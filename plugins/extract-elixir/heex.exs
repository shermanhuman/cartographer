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

  `attrs` is `[{name, value}]` for a component's literal attributes, which
  the walker reads for pass-through event names.
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
          [{:component, :local, name, literal_attrs(attrs), meta.line}]

        :remote_component ->
          [{:component, :remote, name, literal_attrs(attrs), meta.line}]

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

  defp literal_attrs(attrs),
    do: for({n, {:string, s, _}, _} when is_binary(n) <- attrs, do: {n, s})

  defp string({:string, s, _}), do: s
  defp string(_), do: nil

  defp colocated?(attrs) do
    Enum.any?(attrs, fn
      {":type", {:expr, code, _}, _} -> String.ends_with?(String.trim(code), "ColocatedHook")
      _ -> false
    end)
  end

  @doc """
  Literal names a colocated hook's script sends: `{name, "pushEvent"}` for
  `this.pushEvent("name")`, `{name, "pushEventTo"}` for
  `pushEventTo(selector, "name")`.
  """
  def script_pushes(text) do
    to = ~r/\bpushEventTo\s*\(\s*[^,()]+,\s*(["'])([^"'\\]+)\1/
    plain = ~r/\bpushEvent\s*\(\s*(["'])([^"'\\]+)\1/

    Enum.map(Regex.scan(plain, text), fn [_, _, name] -> {name, "pushEvent"} end) ++
      Enum.map(Regex.scan(to, text), fn [_, _, name] -> {name, "pushEventTo"} end)
  end
end
