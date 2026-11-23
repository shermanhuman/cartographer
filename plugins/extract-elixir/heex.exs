defmodule Cartographer.HEEx do
  @moduledoc """
  Reads HEEx templates (`~H` bodies and `.heex` files) with LiveView's own
  tokenizer, vendored in `heex_tokenizer.exs`, and lists what the walker
  needs: component calls, Elixir expressions, event attributes, `phx-hook`
  mounts and colocated hooks. Nothing is compiled or evaluated; expression
  code comes back as text for the walker to parse with its atoms encoder.
  """

  alias Cartographer.HEExTokenizer, as: Tok

  # The tag handler the tokenizer asks to classify names; LiveView's own
  # (Phoenix.LiveView.HTMLEngine) isn't loaded.
  defmodule Tags do
    @moduledoc false
    def classify_type(":inner_block"), do: {:error, "the slot name :inner_block is reserved"}
    def classify_type(":" <> name), do: {:slot, name}

    def classify_type(<<first, _::binary>> = name) when first in ?A..?Z,
      do: {:remote_component, name}

    def classify_type("."), do: {:error, "a component name is required after ."}
    def classify_type("." <> name), do: {:local_component, name}
    def classify_type(name), do: {:tag, name}
    @void ~w(area base br col hr img input link meta param command keygen source track wbr)
    def void?(name), do: name in @void
  end

  # Attributes that send an event to the server.
  @event_attrs ~w(phx-click phx-click-away phx-capture-click phx-submit phx-change phx-blur
                  phx-focus phx-keyup phx-keydown phx-window-keyup phx-window-keydown
                  phx-window-blur phx-window-focus phx-viewport-top phx-viewport-bottom
                  phx-auto-recover)

  @doc """
  Tokenizes a template. `line` is the line its first character is on.
  `{:ok, tokens}` or `{:error, message}` (a syntax error, line included).
  """
  def tokenize(source, file, line) do
    eex =
      case EEx.tokenize(source, line: line, file: file) do
        {:ok, eex} -> eex
        {:error, message, meta} -> throw({:eex, "line #{meta[:line] || line}: #{message}"})
      end

    {tokens, cont} =
      Enum.reduce(eex, {[], {:text, :enabled}}, fn
        {:text, text, meta}, {toks, cont} ->
          state = Tok.init(0, file, source, Tags)

          Tok.tokenize(
            List.to_string(text),
            [line: meta.line, column: meta.column],
            toks,
            cont,
            state
          )

        {type, _marker, expr, meta}, {toks, cont}
        when type in [:start_expr, :expr, :end_expr, :middle_expr] ->
          {[{:eex, type, List.to_string(expr), meta.line} | toks], cont}

        _, acc ->
          acc
      end)

    {:ok, Tok.finalize(tokens, file, cont, source)}
  rescue
    e in [EEx.SyntaxError] ->
      {:error, "line #{e.line}: #{e.message}"}

    e ->
      line =
        case e do
          %{line: l} when is_integer(l) -> "line #{l}: "
          _ -> ""
        end

      {:error, line <> (e |> Map.get(:description, Exception.message(e)) |> to_string())}
  catch
    {:eex, message} -> {:error, message}
  end

  @doc """
  The items of a token list, in order:

    * `{:component, :local | :remote, name, attrs, line}`: `<.name>` or
      `<Mod.name>` (name is the text after `<`, without the dot).
    * `{:live_component, module_code, line}`: `<.live_component module={X}>`.
    * `{:expr, code, line}`: Elixir in `{...}`, `<%= %>` or an attribute.
    * `{:event, attr, value, target, line}`: an event attribute; value and
      target are the raw attribute values (`{:string, s, _}`, `{:expr, code, _}`).
    * `{:hook, value, line}`: a `phx-hook` attribute.
    * `{:colocated_hook, name, script_text, line}`: a
      `<script :type={ColocatedHook} name=".Name">` body.

  `attrs` is `[{name, value}]` for a component's attributes: the literal
  text, or `:dynamic` for code. The walker reads them for pass-through
  event names.
  """
  def items(tokens) do
    # Each token with its successor (a <script> tag's body).
    tokens
    |> Enum.zip(tl(tokens ++ [nil]))
    |> Enum.flat_map(fn {token, next} -> item(token, next) end)
  end

  # `<%= for x <- items(@y) do %>` is half an expression: closed here so
  # its calls are read; middle and end markers hold none.
  defp item({:eex, :start_expr, code, line}, _next), do: [{:expr, code <> "\nend", line}]
  defp item({:eex, :expr, code, line}, _next), do: [{:expr, code, line}]
  defp item({:eex, _, _, _}, _next), do: []
  defp item({:body_expr, code, meta}, _next), do: [{:expr, code, meta.line}]

  defp item({type, name, attrs, meta}, next)
       when type in [:tag, :local_component, :remote_component, :slot] do
    target = Enum.find_value(attrs, fn {n, v, _} -> if n == "phx-target", do: v end)

    own =
      case type do
        :local_component when name == "live_component" ->
          case Enum.find_value(attrs, fn {n, v, _} -> if n == "module", do: v end) do
            {:expr, code, _} -> [{:live_component, code, meta.line}]
            _ -> []
          end

        :local_component ->
          [{:component, :local, name, component_attrs(attrs), meta.line}]

        :remote_component ->
          [{:component, :remote, name, component_attrs(attrs), meta.line}]

        _ ->
          []
      end

    colocated =
      if type == :tag and name == "script" and colocated?(attrs) do
        hook = Enum.find_value(attrs, fn {n, v, _} -> if n == "name", do: string(v) end)

        case next do
          {:text, text, _} when is_binary(hook) -> [{:colocated_hook, hook, text, meta.line}]
          _ -> []
        end
      else
        []
      end

    own ++
      colocated ++
      Enum.flat_map(attrs, fn
        {n, value, ameta} when n in @event_attrs ->
          [{:event, n, value, target, line_of(ameta, meta)}]

        {"phx-hook", value, ameta} ->
          [{:hook, value, line_of(ameta, meta)}]

        {_n, {:expr, code, ameta}, _} ->
          [{:expr, code, ameta.line}]

        _ ->
          []
      end)
  end

  defp item(_, _), do: []

  defp line_of(%{line: l}, _), do: l
  defp line_of(_, meta), do: meta.line

  # A component call's attributes: the literal text, or :dynamic for code.
  defp component_attrs(attrs) do
    for {n, value, _} when is_binary(n) <- attrs,
        v = attr_value(value),
        do: {n, v}
  end

  defp attr_value({:string, s, _}), do: s
  defp attr_value({:expr, _, _}), do: :dynamic
  defp attr_value(_), do: nil

  defp string({:string, s, _}), do: s
  defp string(_), do: nil

  defp colocated?(attrs) do
    Enum.any?(attrs, fn
      {":type", {:expr, code, _}, _} -> String.ends_with?(String.trim(code), "ColocatedHook")
      _ -> false
    end)
  end

  @doc """
  The sends in a colocated hook's script: `{name | :dynamic, via}` for each
  `x.pushEvent(name, ...)` and `x.pushEventTo(selector, name, ...)` in code,
  never in a comment or a string. The name is a string literal with no
  interpolation, else `:dynamic`.
  """
  def script_pushes(text) do
    {code, strings} = mask(text)

    Regex.scan(~r/\b(pushEventTo|pushEvent)\s*\(/, code, return: :index)
    |> Enum.map(fn [{start, len}, {vs, vl}] ->
      via = binary_part(code, vs, vl)
      pos = start + len
      pos = if via == "pushEventTo", do: after_first_arg(code, pos), else: pos
      pos = skip_space(code, pos)

      # A name only when the literal stands alone as the argument
      # ("row_" + id is built at runtime).
      name =
        case pos && Map.get(strings, pos) do
          {content, false, stop} when content != "" ->
            if alone?(code, stop), do: content, else: :dynamic

          _ ->
            :dynamic
        end

      {name, via}
    end)
  end

  # The script with comments, string and template text and regex literals
  # blanked (same byte length, quotes kept), and each string or template
  # literal by its opening quote's offset: {decoded value, interpolated?,
  # offset just past it}.
  # Code inside a template's ${...} is kept, so a send there is found.
  defp mask(text) do
    {_rest, _i, acc} = code(text, 0, %{out: [], strings: %{}, last: :start}, :top)
    {IO.iodata_to_binary(Enum.reverse(acc.out)), acc.strings}
  end

  @regex_words ~w(return typeof instanceof in of new delete void throw case do else yield await)

  defp put(acc, bytes), do: %{acc | out: [bytes | acc.out]}
  defp blank(acc, n), do: put(acc, String.duplicate(" ", n))

  # Code, until the end (:top) or the } closing a ${ (:sub, depth).
  defp code(<<>>, i, acc, _mode), do: {<<>>, i, acc}

  defp code(<<"}", rest::binary>>, i, acc, {:sub, 0}), do: {rest, i + 1, put(acc, "}")}

  defp code(<<"//", rest::binary>>, i, acc, mode) do
    {comment, rest} = take_until(rest, "\n")
    code(rest, i + 2 + byte_size(comment), blank(acc, 2 + byte_size(comment)), mode)
  end

  defp code(<<"/*", rest::binary>>, i, acc, mode) do
    {comment, rest} = take_until(rest, "*/")
    closed = String.starts_with?(rest, "*/")
    rest = if closed, do: binary_part(rest, 2, byte_size(rest) - 2), else: rest
    n = 2 + byte_size(comment) + if(closed, do: 2, else: 0)
    code(rest, i + n, blank(acc, n), mode)
  end

  defp code(<<q, rest::binary>>, i, acc, mode) when q in [?", ?'] do
    {raw, rest, closed} = quoted(rest, q, [])
    n = 1 + byte_size(raw) + if(closed, do: 1, else: 0)

    acc =
      put(acc, <<q>> <> String.duplicate(" ", byte_size(raw)) <> if(closed, do: <<q>>, else: ""))

    acc = %{acc | strings: Map.put(acc.strings, i, {decode(raw), false, i + n}), last: :value}
    code(rest, i + n, acc, mode)
  end

  defp code(<<"`", rest::binary>>, i, acc, mode) do
    {rest, j, acc, text, interp} = template(rest, i + 1, put(acc, "`"), [], false)
    acc = %{acc | strings: Map.put(acc.strings, i, {decode(text), interp, j}), last: :value}
    code(rest, j, acc, mode)
  end

  defp code(<<"/", rest::binary>>, i, acc, mode) do
    if regex_allowed?(acc.last) do
      {body, rest} = regex_body(rest, [], false)
      n = 1 + byte_size(body)
      code(rest, i + n, %{blank(acc, n) | last: :value}, mode)
    else
      code(rest, i + 1, %{put(acc, "/") | last: :punct}, mode)
    end
  end

  defp code(<<c, _::binary>> = bin, i, acc, mode)
       when c in ?a..?z or c in ?A..?Z or c in [?_, ?$] do
    [word] = Regex.run(~r/\A[A-Za-z0-9_$]+/, bin)
    n = byte_size(word)
    rest = binary_part(bin, n, byte_size(bin) - n)
    code(rest, i + n, %{put(acc, word) | last: {:word, word}}, mode)
  end

  defp code(<<c, rest::binary>>, i, acc, mode) do
    last =
      cond do
        c in ~c" \t\r\n" -> acc.last
        c in ?0..?9 or c in ~c")]" -> :value
        true -> :punct
      end

    mode =
      case {mode, c} do
        {{:sub, d}, ?{} -> {:sub, d + 1}
        {{:sub, d}, ?}} -> {:sub, d - 1}
        _ -> mode
      end

    code(rest, i + 1, %{put(acc, <<c>>) | last: last}, mode)
  end

  defp regex_allowed?(:start), do: true
  defp regex_allowed?(:punct), do: true
  defp regex_allowed?({:word, w}), do: w in @regex_words
  defp regex_allowed?(_), do: false

  # A regex literal's body after the opening /, through its flags.
  defp regex_body(<<>>, acc, _class), do: {IO.iodata_to_binary(Enum.reverse(acc)), <<>>}

  defp regex_body(<<"\\", c, rest::binary>>, acc, class),
    do: regex_body(rest, [<<?\\, c>> | acc], class)

  defp regex_body(<<"[", rest::binary>>, acc, false), do: regex_body(rest, ["[" | acc], true)
  defp regex_body(<<"]", rest::binary>>, acc, true), do: regex_body(rest, ["]" | acc], false)

  defp regex_body(<<"/", rest::binary>>, acc, false) do
    [flags] = Regex.run(~r/\A[a-z]*/, rest)
    n = byte_size(flags)

    {IO.iodata_to_binary(Enum.reverse([flags, "/" | acc])),
     binary_part(rest, n, byte_size(rest) - n)}
  end

  defp regex_body(<<"\n", _::binary>> = rest, acc, _class),
    do: {IO.iodata_to_binary(Enum.reverse(acc)), rest}

  defp regex_body(<<c, rest::binary>>, acc, class), do: regex_body(rest, [<<c>> | acc], class)

  # A quoted string's raw body (escapes kept), the rest, and whether it closed.
  defp quoted(<<>>, _q, acc), do: {IO.iodata_to_binary(Enum.reverse(acc)), <<>>, false}
  defp quoted(<<"\\", c, rest::binary>>, q, acc), do: quoted(rest, q, [<<?\\, c>> | acc])

  defp quoted(<<q, rest::binary>>, q, acc),
    do: {IO.iodata_to_binary(Enum.reverse(acc)), rest, true}

  defp quoted(<<"\n", _::binary>> = rest, _q, acc),
    do: {IO.iodata_to_binary(Enum.reverse(acc)), rest, false}

  defp quoted(<<c, rest::binary>>, q, acc), do: quoted(rest, q, [<<c>> | acc])

  # A template literal after its backtick: text blanked, ${...} kept as code.
  defp template(<<>>, i, acc, text, interp),
    do: {<<>>, i, acc, IO.iodata_to_binary(Enum.reverse(text)), interp}

  defp template(<<"`", rest::binary>>, i, acc, text, interp),
    do: {rest, i + 1, put(acc, "`"), IO.iodata_to_binary(Enum.reverse(text)), interp}

  defp template(<<"\\", c, rest::binary>>, i, acc, text, interp),
    do: template(rest, i + 2, blank(acc, 2), [<<?\\, c>> | text], interp)

  defp template(<<"${", rest::binary>>, i, acc, text, _interp) do
    {rest, j, acc} = code(rest, i + 2, %{put(acc, "${") | last: :punct}, {:sub, 0})
    template(rest, j, acc, text, true)
  end

  defp template(<<c, rest::binary>>, i, acc, text, interp),
    do: template(rest, i + 1, blank(acc, 1), [<<c>> | text], interp)

  # A JavaScript string's value from its raw body.
  defp decode(raw) do
    Regex.replace(~r/\\(u\{[0-9a-fA-F]+\}|u[0-9a-fA-F]{4}|x[0-9a-fA-F]{2}|\n|.)/s, raw, fn _, e ->
      case e do
        "u{" <> hex -> <<String.to_integer(String.trim_trailing(hex, "}"), 16)::utf8>>
        "u" <> hex -> <<String.to_integer(hex, 16)::utf8>>
        "x" <> hex -> <<String.to_integer(hex, 16)::utf8>>
        "n" -> "\n"
        "t" -> "\t"
        "r" -> "\r"
        "0" -> <<0>>
        "\n" -> ""
        other -> other
      end
    end)
  end

  defp take_until(text, stop) do
    case :binary.match(text, stop) do
      {at, _} -> {binary_part(text, 0, at), binary_part(text, at, byte_size(text) - at)}
      :nomatch -> {text, ""}
    end
  end

  defp alone?(code, stop) do
    case skip_space(code, stop) do
      nil -> false
      at -> :binary.at(code, at) in ~c",)"
    end
  end

  defp skip_space(code, pos) when pos < byte_size(code) do
    if :binary.at(code, pos) in ~c" \t\r\n", do: skip_space(code, pos + 1), else: pos
  end

  defp skip_space(_code, _pos), do: nil

  # Past the first argument's top-level comma, or nil.
  defp after_first_arg(code, pos), do: after_first_arg(code, pos, 0)

  defp after_first_arg(code, pos, _depth) when pos >= byte_size(code), do: nil

  defp after_first_arg(code, pos, depth) do
    case :binary.at(code, pos) do
      c when c in ~c"([{" -> after_first_arg(code, pos + 1, depth + 1)
      c when c in ~c")]}" and depth == 0 -> nil
      c when c in ~c")]}" -> after_first_arg(code, pos + 1, depth - 1)
      ?, when depth == 0 -> pos + 1
      _ -> after_first_arg(code, pos + 1, depth)
    end
  end
end
