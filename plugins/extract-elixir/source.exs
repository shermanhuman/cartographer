defmodule Cartographer.ElixirSource do
  @moduledoc """
  Source-only Elixir AST walker. Parses files with `Code.string_to_quoted/2`
  and never compiles or executes project code.

  It records modules, one entry per function clause, raw call sites (with the
  lexical aliases and imports in scope, and their argument AST), Ecto
  `schema "table"` declarations, Phoenix router routes and notes. Clauses of
  a module that calls `Req` keep their AST (`:http`) for
  `Cartographer.ElixirHTTP`. Call attribution that needs every definition in the repository
  (local vs imported vs Kernel) happens later in `Cartographer.ElixirFacts`.

  Parsing never creates atoms for names found in the source: a static atoms
  encoder returns the existing atom when there is one and `%{__atom__: name}`
  otherwise, so a large or hostile repository cannot exhaust the atom table.
  Everything downstream reads names through `str/1` and renders AST with
  `print/1` and `normalize/1`, which treat both forms alike, so output does
  not depend on which atoms happen to exist.
  """

  @defs [:def, :defp, :defmacro, :defmacrop, :defdelegate, :defguard, :defguardp]
  @special [
    :__block__,
    :__aliases__,
    :{},
    :%{},
    :%,
    :<<>>,
    :">>",
    :"<<",
    :.,
    :&,
    :fn,
    :->,
    :when,
    :do,
    :end,
    :quote,
    :unquote,
    :unquote_splicing,
    :alias,
    :import,
    :require,
    :use,
    :@,
    :|>,
    :=,
    :^,
    :for,
    :with,
    :case,
    :cond,
    :if,
    :unless,
    :receive,
    :try
  ]
  @special_forms Kernel.SpecialForms.__info__(:macros)

  # Stop parsing when the VM's atom table is this full. The encoder keeps
  # parsing from interning source names, so this is a backstop only.
  @atom_budget div(:erlang.system_info(:atom_limit) * 9, 10)

  # A name from the source: an existing atom or an encoded one.
  defguardp ident?(x) when is_atom(x) or (is_map(x) and is_map_key(x, :__atom__))

  @doc "The text of a name that is an atom or an encoded `%{__atom__: name}`."
  def str(%{__atom__: name}), do: name
  def str(atom) when is_atom(atom), do: Atom.to_string(atom)

  @doc false
  def encode_atom(name, _meta) do
    {:ok, String.to_existing_atom(name)}
  rescue
    ArgumentError -> {:ok, %{__atom__: name}}
  end

  def new,
    do: %{
      modules: [],
      clauses: [],
      calls: [],
      schemas: [],
      notes: [],
      routes: [],
      route_skips: [],
      # Templates and LiveView events (see template/5).
      events: [],
      handlers: [],
      hook_mounts: [],
      passthrough: [],
      uses: [],
      module_envs: [],
      embeds: [],
      attrs: []
    }

  # Req functions that send a request; their URL-or-options arguments are
  # where a wrapper's path parameter ends up (see Cartographer.ElixirHTTP).
  @req_sinks ~w(request get post put patch delete head request! get! post! put! patch! delete! head!)
  def req_sinks, do: @req_sinks

  @doc """
  Parses `source` (the contents of `relative`) and adds its facts to `acc`.
  `opts[:atom_budget]` overrides the atom-table backstop (tests).
  """
  def parse(acc, source, relative, repo, opts \\ []) do
    budget = Keyword.get(opts, :atom_budget, @atom_budget)

    if :erlang.system_info(:atom_count) > budget do
      note(
        acc,
        "atom_budget",
        relative,
        "not parsed: the atom table is past its budget (#{budget} of #{:erlang.system_info(:atom_limit)})"
      )
    else
      parse_source(acc, source, relative, repo, Keyword.get(opts, :helpers, %{}))
    end
  end

  defp parse_source(acc, source, relative, repo, helpers) do
    env = %{
      module: nil,
      aliases: %{},
      imports: %{},
      # the modules `use`d in this lexical scope, directly or through an
      # expanded helper (newest first): a bare call's library owners
      uses: [],
      file: relative,
      repo: repo,
      caller: nil,
      helpers: helpers
    }

    result =
      try do
        Code.string_to_quoted(source,
          columns: true,
          token_metadata: true,
          emit_warnings: false,
          file: relative,
          static_atoms_encoder: &__MODULE__.encode_atom/2
        )
      rescue
        e -> {:error, Exception.message(e)}
      end

    case result do
      {:ok, ast} ->
        {_, file_acc} = walk(ast, env, new())
        merge(acc, prune_http(file_acc))

      {:error, reason} ->
        note(acc, "parse_error", relative, parse_message(reason))
    end
  end

  # Clause ASTs are kept only for modules with a Req request call: the HTTP
  # wrapper analysis reads nothing else, and the rest would only hold memory.
  defp prune_http(acc) do
    req_modules =
      for c <- acc.calls,
          c.module == "Req" and c.name in @req_sinks,
          into: MapSet.new(),
          do: c.caller_module

    clauses =
      Enum.map(acc.clauses, fn c ->
        if MapSet.member?(req_modules, c.module), do: c, else: Map.delete(c, :http)
      end)

    %{acc | clauses: clauses}
  end

  defp merge(acc, file_acc),
    do: Map.new(acc, fn {k, v} -> {k, Map.get(file_acc, k, []) ++ v} end)

  defp parse_message({meta, message, token}) when is_list(meta) do
    message =
      case message do
        {prefix, suffix} -> "#{prefix}#{suffix}"
        m when is_binary(m) -> m
        other -> inspect(other)
      end

    "line #{meta[:line] || "?"}: #{message}#{print_token(token)}"
  end

  defp parse_message(other) when is_binary(other), do: other
  defp parse_message(other), do: inspect(other)

  defp print_token(token) when is_binary(token), do: token
  defp print_token(token) when is_list(token), do: List.to_string(token)
  defp print_token(token), do: print(token)

  defp note(acc, kind, file, message),
    do: %{acc | notes: [%{kind: kind, file: file, message: message} | acc.notes]}

  # Module names. Erlang modules keep their leading colon (":ets").
  defp name({:__aliases__, _, parts}, env) do
    parts =
      Enum.map(parts, fn
        {:__MODULE__, _, _} -> env.module || "__MODULE__"
        p when ident?(p) -> p |> str() |> String.trim_leading("Elixir.")
        p -> print(p)
      end)

    first = hd(parts)

    Enum.join([Map.get(env.aliases, first, first) | tl(parts)], ".")
    |> String.trim_leading("Elixir.")
  end

  defp name({:__MODULE__, _, _}, env), do: env.module || "__MODULE__"

  defp name(atom, _) when ident?(atom) do
    case str(atom) do
      "Elixir." <> rest -> rest
      erlang -> ":" <> erlang
    end
  end

  defp name(ast, _), do: print(ast)

  defp literal_module?(mod),
    do: match?({:__aliases__, _, _}, mod) or match?({:__MODULE__, _, _}, mod) or ident?(mod)

  defp part(p) when ident?(p), do: str(p)
  defp part(p), do: print(p)

  defp line(meta), do: Keyword.get(meta, :line, 1)
  defp id(env, fun, arity), do: "#{env.repo}:#{env.module}.#{str(fun)}/#{arity}"

  # Sequential module forms establish lexical aliases/imports. Generic AST
  # descent must not discard this environment between sibling expressions.
  defp walk({:__block__, _, forms}, env, acc), do: walk(forms, env, acc)
  # Quoted code is data, not an executed call edge; macro expansion is outside
  # this source-only inventory. Do not invent runtime edges from templates.
  defp walk({:quote, _, _}, env, acc), do: {env, acc}

  # Block keywords are sibling lexical scopes. Aliases introduced in `do`
  # must not change resolution in `else`, `rescue`, `catch`, or `after`.
  defp walk({block, body}, env, acc) when block in [:do, :else, :rescue, :catch, :after] do
    {_, acc} = walk(body, env, acc)
    {env, acc}
  end

  defp walk({:defmodule, meta, [mod, body]}, env, acc) do
    # defmodule aliases are lexical declarations, not ordinary alias lookup:
    # an inner A.B is Outer.A.B even if A was aliased to another module.
    module =
      case mod do
        {:__aliases__, _, [Elixir | rest]} ->
          Enum.map_join(rest, ".", &part/1)

        {:__aliases__, _, [{:__MODULE__, _, _} | rest]} ->
          Enum.join(Enum.reject([env.module | Enum.map(rest, &part/1)], &is_nil/1), ".")

        {:__aliases__, _, parts} ->
          Enum.join(Enum.reject([env.module | Enum.map(parts, &part/1)], &is_nil/1), ".")

        other ->
          name(other, env)
      end

    # A nested defmodule also defines an alias in the enclosing scope.
    outer =
      case mod do
        {:__aliases__, _, [first | _]}
        when ident?(first) and first != Elixir and env.module != nil ->
          short = str(first)
          %{env | aliases: Map.put(env.aliases, short, env.module <> "." <> short)}

        _ ->
          env
      end

    next = %{outer | module: module, caller: nil}

    acc = %{
      acc
      | modules: [%{name: module, file: env.file, line: line(meta)} | acc.modules]
    }

    # The module body's own scope, walked so its final aliases and imports
    # (those a .heex template of the module sees) can be kept.
    {inner, acc} =
      case body do
        [{:do, block}] -> walk(block, next, acc)
        _ -> walk(body, next, acc)
      end

    acc = %{
      acc
      | module_envs: [
          %{module: module, aliases: inner.aliases, imports: inner.imports, file: env.file}
          | acc.module_envs
        ]
    }

    acc =
      if router?(body), do: route_walk(body[:do], next, %{path: [], alias: nil}, acc), else: acc

    {outer, acc}
  end

  # defprotocol expands to defmodule: same naming and alias rules.
  defp walk({:defprotocol, meta, [mod, body]}, env, acc),
    do: walk({:defmodule, meta, [mod, body]}, env, acc)

  # defimpl P, for: T defines the module P.T (not nested in the enclosing
  # module); `for: [A, B]` defines one module each, and a defimpl without
  # `for:` inside a module implements the protocol for that module. Its
  # functions belong to the impl module, never to the host.
  defp walk({:defimpl, meta, [protocol | rest]}, env, acc) do
    opts = Enum.flat_map(rest, &if(Keyword.keyword?(&1), do: &1, else: []))
    proto = name(protocol, env)

    targets =
      case Keyword.fetch(opts, :for) do
        {:ok, list} when is_list(list) -> list
        {:ok, one} -> [one]
        :error when env.module != nil -> [{:__MODULE__, [], nil}]
        :error -> []
      end

    acc =
      if targets == [],
        do:
          note(
            acc,
            "dynamic_definition",
            env.file,
            "line #{line(meta)}: defimpl #{proto} without for: outside a module is not inventoried"
          ),
        else: acc

    acc =
      Enum.reduce(targets, acc, fn target, acc ->
        if literal_module?(target) and literal_module?(protocol) do
          module =
            String.trim_leading(proto, ":") <> "." <> String.trim_leading(name(target, env), ":")

          acc = %{
            acc
            | modules: [%{name: module, file: env.file, line: line(meta)} | acc.modules]
          }

          {_, acc} = walk([do: opts[:do]], %{env | module: module, caller: nil}, acc)
          acc
        else
          note(
            acc,
            "dynamic_definition",
            env.file,
            "line #{line(meta)}: defimpl #{proto} for #{print(target)} is not inventoried"
          )
        end
      end)

    {env, acc}
  end

  # A template inside a function: its components, expressions and events
  # belong to that function.
  defp walk({:sigil_H, meta, [{:<<>>, _, [text]}, _mods]}, env, acc) when is_binary(text) do
    heredoc = Keyword.get(meta, :delimiter) in [~s("""), "'''"]
    first = if heredoc, do: line(meta) + 1, else: line(meta)
    {env, template(acc, env, text, first)}
  end

  # `use X, :kind` and `use X`: the imports and aliases X's __using__ quote
  # brings in (one level of an in-repo helper, as Phoenix's `html_helpers`),
  # so template components resolve per app.
  defp walk({:use, _, [mod | rest]}, %{module: m, caller: nil} = env, acc) when m != nil do
    kind =
      case rest do
        [k | _] when ident?(k) -> str(k)
        _ -> nil
      end

    expand_use(name(mod, env), kind, env, acc, 0)
  end

  defp walk({:embed_templates, meta, [pattern | rest]}, %{module: m, caller: nil} = env, acc)
       when m != nil and is_binary(pattern) do
    # `suffix: "_html"` is added to each function name.
    suffix =
      case rest do
        [opts] when is_list(opts) ->
          Enum.find_value(opts, "", fn
            {k, v} when is_binary(v) -> if ident?(k) and str(k) == "suffix", do: v
            _ -> nil
          end)

        _ ->
          ""
      end

    {env,
     %{
       acc
       | embeds: [
           %{module: m, pattern: pattern, suffix: suffix, file: env.file, line: line(meta)}
           | acc.embeds
         ]
     }}
  end

  # A module attribute holding literal strings (`@events ~w(a b)`), which a
  # handle_event guard may name.
  defp walk({:@, _, [{attr, _, [value]}]}, %{module: m, caller: nil} = env, acc)
       when m != nil and ident?(attr) do
    # A single string (`@api "https://..."`) is kept too: an HTTP base URL.
    acc =
      case literal_strings(value) do
        {:ok, list} -> %{acc | attrs: [%{module: m, name: str(attr), values: list} | acc.attrs]}
        :error when is_binary(value) -> %{acc | attrs: [%{module: m, name: str(attr), values: [value]} | acc.attrs]}
        :error -> acc
      end

    {_, acc} = walk(value, env, acc)
    {env, acc}
  end

  defp walk({:alias, _, [{{:., _, [prefix, :{}]}, _, items}]}, env, acc) do
    aliases =
      Enum.reduce(items, env.aliases, fn item, map ->
        full = name(prefix, env) <> "." <> name(item, %{env | aliases: %{}})
        Map.put(map, List.last(String.split(full, ".")), full)
      end)

    {%{env | aliases: aliases}, acc}
  end

  defp walk({:alias, _, [mod | opts]}, env, acc) do
    full = name(mod, env)
    opt = List.first(opts) || []

    short =
      if Keyword.keyword?(opt) && Keyword.has_key?(opt, :as),
        do: name(opt[:as], %{env | aliases: %{}}),
        else: List.last(String.split(full, "."))

    {%{env | aliases: Map.put(env.aliases, short, full)}, acc}
  end

  defp walk({:require, _, [mod, opts]}, env, acc) when is_list(opts) do
    if Keyword.keyword?(opts) && Keyword.has_key?(opts, :as),
      do: walk({:alias, [], [mod, [as: opts[:as]]]}, env, acc),
      else: {env, acc}
  end

  # A later import of the same module replaces the earlier one in this
  # lexical scope (it does not add to it); see import_entry/2.
  defp walk({:import, _, [mod | opts]}, env, acc) do
    module = name(mod, env)
    prior = Map.get(env.imports, module, if(module == "Kernel", do: kernel_import()))
    entry = import_entry(List.first(opts) || [], prior)
    {%{env | imports: Map.put(env.imports, module, entry)}, acc}
  end

  # A bitstring segment's type (`binary-size(32)`, `integer-little`) names
  # modifiers, not calls: only its value is code.
  defp walk({:"::", _, [value, _type]}, env, acc), do: walk(value, env, acc)

  defp walk({kind, meta, [head | body]} = clause, env, acc) when kind in @defs do
    {head, guards} =
      case head do
        {:when, _, [h | guards]} -> {h, guards}
        h -> {h, []}
      end

    case head do
      {fun, _, args}
      when ident?(fun) and (is_list(args) or is_nil(args)) and env.module != nil ->
        args = args || []
        arity = length(args)
        defaults = Enum.count(args, &match?({:\\, _, _}, &1))
        fid = id(env, fun, arity)

        c = %{
          id: fid,
          module: env.module,
          name: str(fun),
          arity: arity,
          min_arity: arity - defaults,
          kind: to_string(kind),
          file: env.file,
          line: line(meta),
          end_line: max_line(clause),
          normalized: normalize(clause),
          http: %{params: args, guards: guards, body: body, aliases: env.aliases}
        }

        acc = %{acc | clauses: [c | acc.clauses]}

        acc =
          if str(fun) == "handle_event" and arity == 3,
            do: handler(acc, env, fid, args, guards, body, meta),
            else: acc

        next = %{env | caller: fid}
        {_, acc} = walk(body, next, acc)
        {_, acc} = walk(args, next, acc)
        {_, acc} = walk(guards, next, acc)

        acc =
          if kind == :defdelegate do
            opts = List.first(body) || []

            if Keyword.keyword?(opts) && opts[:to],
              do:
                call(acc, next, name(opts[:to], env), opts[:as] || fun, arity, meta, "delegate"),
              else: acc
          else
            acc
          end

        {env, acc}

      {fun, _, args} when ident?(fun) and (is_list(args) or is_nil(args)) ->
        {env,
         note(
           acc,
           "dynamic_definition",
           env.file,
           "line #{line(meta)}: #{kind} #{str(fun)} outside a module is not inventoried"
         )}

      _ ->
        {env,
         note(
           acc,
           "dynamic_definition",
           env.file,
           "line #{line(meta)}: unresolved generated definition: " <> print(head)
         )}
    end
  end

  defp walk({:schema, meta, [table, body]}, env, acc) when is_binary(table) do
    acc = %{
      acc
      | schemas: [
          %{module: env.module, table: table, file: env.file, line: line(meta)} | acc.schemas
        ]
    }

    # Associations and embedded calls in a schema body are module-level, not
    # function calls; walk them only to keep lexical state consistent.
    {_, acc} = walk(body, %{env | caller: nil}, acc)
    {env, acc}
  end

  # The pipe placeholder below: its left side was walked already. Without
  # this clause the tuple fallback walks the left side again through the
  # placeholder's metadata, doubling the work at every step of a pipe chain
  # (2^n calls for n pipes).
  defp walk({:__pipe_arg__, _, nil}, env, acc), do: {env, acc}

  defp walk({:|>, _, [left, right]}, env, acc) do
    {_, acc} = walk(left, env, acc)
    # The piped value stands first in the call's arguments as a placeholder
    # (walked as a variable, so the left side's calls aren't recorded twice)
    # that carries the left side for readers of the arguments: a literal
    # path piped into a Req wrapper is still that path.
    piped = {:__pipe_arg__, [value: left], nil}

    case right do
      {{:., dotmeta, [mod, fun]}, meta, args} when is_list(args) ->
        walk({{:., dotmeta, [mod, fun]}, meta, [piped | args]}, env, acc)

      {fun, meta, args} when ident?(fun) and is_list(args) ->
        walk({fun, meta, [piped | args]}, env, acc)

      _ ->
        walk(right, env, acc)
    end
  end

  defp walk({:&, _, [{:/, _, [{{:., _, [mod, fun]}, meta, _}, arity]}]}, env, acc)
       when ident?(fun) and is_integer(arity) do
    kind = if literal_module?(mod), do: "capture", else: "dynamic"
    module = if literal_module?(mod), do: name(mod, env), else: nil
    {env, call(acc, env, module, fun, arity, meta, kind)}
  end

  defp walk({:&, _, [{:/, _, [{fun, meta, _}, arity]}]}, env, acc)
       when ident?(fun) and is_integer(arity),
       do: {env, call(acc, env, nil, fun, arity, meta, "capture")}

  defp walk({{:., _, [mod, fun]}, meta, args}, env, acc) when ident?(fun) and is_list(args) do
    literal = literal_module?(mod)

    acc =
      cond do
        # `map.field` without parentheses is field access, not a call.
        not literal and args == [] and Keyword.get(meta, :no_parens, false) ->
          acc

        # "#{x}" expands to Kernel.to_string(x); not a call in the source.
        Keyword.get(meta, :from_interpolation, false) ->
          acc

        literal and name(mod, env) in ["Kernel", ":erlang"] and fun == :apply ->
          apply_call(acc, env, args, meta)

        literal ->
          call(acc, env, name(mod, env), fun, length(args), meta, "remote", args)

        true ->
          call(acc, env, nil, fun, length(args), meta, "dynamic")
      end

    {_, acc} = walk(mod, env, acc)
    {_, acc} = walk(args, env, acc)
    {env, acc}
  end

  defp walk({{:., _, [fun_expr]}, meta, args}, env, acc) when is_list(args) do
    acc = call(acc, env, nil, :anonymous, length(args), meta, "dynamic")
    {_, acc} = walk(fun_expr, env, acc)
    {_, acc} = walk(args, env, acc)
    {env, acc}
  end

  defp walk({:apply, meta, args}, env, acc) when is_list(args) and length(args) in [2, 3] do
    acc = apply_call(acc, env, args, meta)
    {_, acc} = walk(args, env, acc)
    {env, acc}
  end

  defp walk({fun, meta, args}, env, acc) when ident?(fun) and is_list(args) do
    arity = length(args)

    acc =
      if env.caller && fun not in @special && {fun, arity} not in @special_forms &&
           not (is_atom(fun) and Macro.operator?(fun, arity)),
         do: call(acc, env, nil, fun, arity, meta, "local_or_import", args),
         else: acc

    {_, acc} = walk(args, env, acc)
    {env, acc}
  end

  defp walk(items, env, acc) when is_list(items),
    do: Enum.reduce(items, {env, acc}, fn item, {e, a} -> walk(item, e, a) end)

  defp walk(tuple, env, acc) when is_tuple(tuple), do: walk(Tuple.to_list(tuple), env, acc)
  defp walk(_, env, acc), do: {env, acc}

  # --- templates and LiveView events ------------------------------------------

  @doc """
  Reads one HEEx template rendered by `env.caller` (the function that owns
  it): component calls become calls marked `component`, expressions are
  walked as code, and event attributes, hook mounts and colocated hooks are
  recorded for `Cartographer.ElixirFacts` to resolve.
  """
  def template(acc, %{caller: nil}, _text, _first), do: acc

  def template(acc, env, text, first) do
    case Cartographer.HEEx.tokenize(text, env.file, first) do
      {:error, message} ->
        note(acc, "heex_parse_error", env.file, message)

      {:ok, tokens} ->
        tokens
        |> Cartographer.HEEx.items()
        |> Enum.reduce(acc, fn item, acc -> template_item(item, env, acc) end)
    end
  end

  defp template_item({:component, :local, fun, attrs, l}, env, acc) do
    component_call(acc, env, nil, fun, l, attrs)
  end

  defp template_item({:component, :remote, dotted, attrs, l}, env, acc) do
    parts = String.split(dotted, ".")
    {mod, [fun]} = Enum.split(parts, -1)
    module = name({:__aliases__, [], Enum.map(mod, &atomish/1)}, env)
    component_call(acc, env, module, fun, l, attrs)
  end

  defp template_item({:live_component, code, l}, env, acc) do
    case expr(code, l) do
      {:ok, {:__aliases__, _, _} = mod} ->
        component_call(acc, env, name(mod, env), "render", l, [])

      _ ->
        acc
    end
  end

  defp template_item({:expr, code, l}, env, acc) do
    case expr(code, l) do
      {:ok, ast} ->
        acc = js_pushes(ast, env, l, acc)
        {_, acc} = walk(ast, env, acc)
        acc

      :error ->
        acc
    end
  end

  defp template_item({:event, attr, value, target, l}, env, acc) do
    target = target_of(target)

    # The attribute's code is code: its calls are calls (show_modal("x")).
    acc =
      with {:expr, code, _} <- value,
           {:ok, ast} <- expr(code, l) do
        {_, acc} = walk(ast, env, acc)
        acc
      else
        _ -> acc
      end

    case event_value(value, env, l) do
      {:names, names} ->
        Enum.reduce(names, acc, &add_event(&2, env, attr, &1, target, l))

      {:pushes, pushes} ->
        Enum.reduce(pushes, acc, fn {name, push_target}, acc ->
          add_event(
            acc,
            env,
            "JS.push",
            name,
            if(push_target == "", do: target, else: push_target),
            l
          )
        end)

      {:assign, assign} ->
        # A component sending whatever event its caller names: resolved
        # through the caller's literal attribute (see ElixirFacts).
        %{
          acc
          | passthrough: [
              %{
                component: env.caller,
                assign: assign,
                via: attr,
                target: target,
                file: env.file,
                line: l
              }
              | acc.passthrough
            ]
        }

      :client_only ->
        acc

      :dynamic ->
        add_event(acc, env, attr, :dynamic, target, l)
    end
  end

  defp template_item({:hook, value, l}, env, acc) do
    names =
      case value do
        {:string, s, _} ->
          [s]

        {:expr, code, _} ->
          with({:ok, ast} <- expr(code, l), do: branch_strings(ast), else: (_ -> []))

        _ ->
          []
      end

    Enum.reduce(names, acc, fn hook, acc ->
      # A colocated `.Name` hook is scoped to its module.
      full = if String.starts_with?(hook, "."), do: env.module <> hook, else: hook

      %{
        acc
        | hook_mounts: [
            %{hook: full, from: env.caller, file: env.file, line: l} | acc.hook_mounts
          ]
      }
    end)
  end

  # A colocated hook's script runs in the element that renders it, so its
  # literal pushEvent goes where that template's events go.
  # pushEventTo goes to whatever its selector matches: a dynamic target.
  defp template_item({:colocated_hook, _hook, text, l}, env, acc) do
    text
    |> Cartographer.HEEx.script_pushes()
    |> Enum.reduce(acc, fn {name, via}, acc ->
      add_event(acc, env, via, name, if(via == "pushEventTo", do: "dynamic", else: ""), l)
    end)
  end

  defp component_call(acc, env, module, fun, l, attrs) do
    kind = if module, do: "remote", else: "local_or_import"
    acc = call(acc, env, module, atomish(fun), 1, [line: l], kind)

    case acc.calls do
      [c | rest] when c.from == env.caller ->
        %{acc | calls: [Map.merge(c, %{component: true, attrs: attrs}) | rest]}

      _ ->
        acc
    end
  end

  # LiveView's own events (lv:clear-flash) aren't the app's to handle.
  defp add_event(acc, _env, _via, "lv:" <> _, _target, _l), do: acc
  # An empty name is no name.
  defp add_event(acc, env, via, "", target, l), do: add_event(acc, env, via, :dynamic, target, l)

  defp add_event(acc, env, via, name, target, l) do
    e = %{
      from: env.caller,
      module: env.module,
      via: via,
      target: target,
      file: env.file,
      line: l,
      event: if(name == :dynamic, do: nil, else: name),
      dynamic: name == :dynamic
    }

    %{acc | events: [e | acc.events]}
  end

  defp expr(code, l) do
    case Code.string_to_quoted(code,
           line: l,
           columns: true,
           emit_warnings: false,
           file: "template",
           static_atoms_encoder: &__MODULE__.encode_atom/2
         ) do
      {:ok, ast} -> {:ok, ast}
      _ -> :error
    end
  rescue
    _ -> :error
  end

  # A name from template text, as the parser would give it.
  defp atomish(text) do
    {:ok, a} = encode_atom(text, [])
    a
  end

  defp target_of(nil), do: ""

  defp target_of({:expr, code, _}) do
    case expr(code, 1) do
      {:ok, {:@, _, [{name, _, ctx}]}} when ident?(name) and is_atom(ctx) ->
        if str(name) == "myself", do: "myself", else: "dynamic"

      _ ->
        "dynamic"
    end
  end

  defp target_of(_), do: "dynamic"

  # An event attribute's value: literal names, JS.push calls, an assign
  # (pass-through), a client-only JS chain, or something dynamic.
  defp event_value(nil, _env, _l), do: :client_only
  defp event_value({:string, s, _}, _env, _l), do: {:names, [s]}

  defp event_value({:expr, code, _}, env, l) do
    case expr(code, l) do
      {:ok, s} when is_binary(s) ->
        {:names, [s]}

      {:ok, {:@, _, [{name, _, ctx}]}} when ident?(name) and is_atom(ctx) ->
        {:assign, str(name)}

      {:ok, ast} ->
        pushes = push_calls(ast, env)

        cond do
          pushes != [] -> {:pushes, pushes}
          js_call?(ast, env) -> :client_only
          (names = branch_strings(ast)) != [] -> {:names, names}
          true -> :dynamic
        end

      :error ->
        :dynamic
    end
  end

  defp event_value(_, _env, _l), do: :dynamic

  # JS.push calls in an expression not on an event attribute
  # (dismiss={JS.push("dismiss_notice")}).
  defp js_pushes(ast, env, l, acc) do
    ast
    |> push_calls(env)
    |> Enum.reduce(acc, fn {name, target}, acc ->
      add_event(acc, env, "JS.push", name, target, l)
    end)
  end

  defp js_module?(mod, env), do: name(mod, env) in ["JS", "Phoenix.LiveView.JS"]

  # [{name | :dynamic, target}] for each JS.push in ast.
  defp push_calls(ast, env) do
    {_, found} =
      Macro.prewalk(ast, [], fn
        {{:., _, [mod, fun]}, _, args} = node, acc when is_list(args) and ident?(fun) ->
          if str(fun) == "push" and js_module?(mod, env) do
            {node, [push_info(args) | acc]}
          else
            {node, acc}
          end

        node, acc ->
          {node, acc}
      end)

    Enum.reverse(found)
  end

  defp push_info(args) do
    # JS.push("e"), JS.push(js, "e"), JS.push("e", target: ...).
    name = Enum.find(args, &is_binary/1) || :dynamic

    opts =
      Enum.find(args, [], fn a ->
        is_list(a) and a != [] and Enum.all?(a, &match?({k, _} when ident?(k), &1))
      end)

    # By name: another option's key may be an encoded (unknown) atom.
    target =
      case Enum.find(opts, fn {k, _} -> ident?(k) and str(k) == "target" end) do
        nil ->
          ""

        {_, {:@, _, [{n, _, ctx}]}} when ident?(n) and is_atom(ctx) ->
          if(str(n) == "myself", do: "myself", else: "dynamic")

        _ ->
          "dynamic"
      end

    {name, target}
  end

  defp js_call?(ast, env) do
    {_, found} =
      Macro.prewalk(ast, false, fn
        {{:., _, [mod, _]}, _, _} = node, acc -> {node, acc or js_module?(mod, env)}
        node, acc -> {node, acc}
      end)

    found
  end

  # The literal results of `if`/`unless`/`case`/`cond` branches (never the
  # condition), or a plain literal.
  defp branch_strings(s) when is_binary(s), do: [s]

  defp branch_strings({kind, _, [_cond, opts]}) when kind in [:if, :unless] and is_list(opts) do
    Enum.flat_map([:do, :else], fn k ->
      case List.keyfind(opts, k, 0) do
        {_, v} -> branch_strings(v)
        nil -> []
      end
    end)
  end

  defp branch_strings({kind, _, args}) when kind in [:case, :cond] and is_list(args) do
    clauses =
      case List.last(args) do
        [{:do, list}] when is_list(list) -> list
        _ -> []
      end

    Enum.flat_map(clauses, fn
      {:->, _, [_, rhs]} -> branch_strings(rhs)
      _ -> []
    end)
  end

  defp branch_strings({:__block__, _, [one]}), do: branch_strings(one)
  defp branch_strings(_), do: []

  # `~w(a b)`, `["a", "b"]` or `"a"`: the strings, else :error.
  def literal_strings({:sigil_w, _, [{:<<>>, _, [text]}, mods]})
      when is_binary(text) and mods in [[], ~c"s"],
      do: {:ok, String.split(text)}

  def literal_strings(list) when is_list(list) do
    if list != [] and Enum.all?(list, &is_binary/1), do: {:ok, list}, else: :error
  end

  def literal_strings(_), do: :error

  # The event names one handle_event clause matches: its head literal, or a
  # guard on its first parameter (`when event in ~w(...)`, `in [...]`,
  # `== "x"`, `in @attr`); a catch-all whose body hands the event to
  # `Mod.handle_event(event, ...)` names that delegate.
  defp handler(acc, env, fid, [first | _], guards, body, meta) do
    l = line(meta)

    entries =
      case first do
        s when is_binary(s) and s != "" ->
          [%{function: fid, event: s, source: "head", line: l}]

        {var, _, ctx} when ident?(var) and is_atom(ctx) ->
          guarded = Enum.flat_map(guards, &guard_names(&1, var, env))

          # A clause may name its events in a guard and hand them on too.
          # An empty name is no name.
          guarded = Enum.reject(guarded, &(&1 == ""))

          Enum.map(guarded, fn
            {:attr, name} ->
              %{function: fid, attr: name, module: env.module, source: "attribute", line: l}

            s ->
              %{function: fid, event: s, source: "guard", line: l}
          end) ++
            for(
              mod <- delegates(body, var, env),
              do: %{function: fid, delegate_module: mod, source: "delegate", line: l}
            )

        _ ->
          []
      end

    %{acc | handlers: entries ++ acc.handlers}
  end

  defp guard_names({op, _, [l, r]}, var, env) when op in [:or, :and, :when],
    do: guard_names(l, var, env) ++ guard_names(r, var, env)

  defp guard_names({:in, _, [{v, _, ctx}, right]}, var, _env) when ident?(v) and is_atom(ctx) do
    if str(v) == str(var) do
      case right do
        {:@, _, [{attr, _, ctx2}]} when ident?(attr) and is_atom(ctx2) -> [{:attr, str(attr)}]
        other -> with({:ok, list} <- literal_strings(other), do: list, else: (_ -> []))
      end
    else
      []
    end
  end

  defp guard_names({:==, _, [{v, _, ctx}, s]}, var, _env)
       when ident?(v) and is_atom(ctx) and is_binary(s),
       do: if(str(v) == str(var), do: [s], else: [])

  defp guard_names(_, _, _), do: []

  defp delegates(body, var, env) do
    {_, found} =
      Macro.prewalk(body, [], fn
        {{:., _, [mod, fun]}, _, [{v, _, ctx} | _]} = node, acc
        when ident?(fun) and ident?(v) and is_atom(ctx) ->
          if str(fun) == "handle_event" and str(v) == str(var) and literal_module?(mod),
            do: {node, [name(mod, env) | acc]},
            else: {node, acc}

        node, acc ->
          {node, acc}
      end)

    found |> Enum.reverse() |> Enum.uniq()
  end

  # `use` expansion: the forms of an in-repo module's __using__ quote (or,
  # for `use X, :kind`, of its `kind/0` quote), applied to this module's
  # scope. `unquote(helper())` follows another quote of the same module;
  # a nested `use` follows up to three levels.
  defp expand_use(module, kind, env, acc, depth) do
    acc = %{acc | uses: [%{module: env.module, use: module, kind: kind} | acc.uses]}
    h = Map.get(env.helpers, module)
    env = %{env | uses: [module | Map.get(env, :uses, [])]}

    # An ExUnit.CaseTemplate's `using` block runs inside `use ExUnit.Case`,
    # which the block itself doesn't name.
    env =
      if h && Map.get(h, :template, false),
        do: %{env | uses: ["ExUnit.Case" | env.uses]},
        else: env

    forms =
      cond do
        h == nil or depth >= 3 -> []
        kind && h.using == :dispatch -> Map.get(h.funs, kind, [])
        is_list(h.using) -> h.using
        true -> []
      end

    Enum.reduce(forms, {env, acc}, fn form, {e, a} ->
      expand_form(form, module, h, e, a, depth)
    end)
  end

  defp expand_form({kind, _, _} = form, _module, _h, env, acc, _depth)
       when kind in [:import, :alias, :require],
       do: walk(form, env, acc)

  defp expand_form({:use, _, [mod | rest]}, _module, _h, env, acc, depth) do
    kind =
      case rest do
        [k | _] when ident?(k) -> str(k)
        _ -> nil
      end

    expand_use(name(mod, env), kind, env, acc, depth + 1)
  end

  defp expand_form({:unquote, _, [{fun, _, args}]}, module, h, env, acc, depth)
       when ident?(fun) and args in [nil, []] do
    forms = Map.get(h.funs, str(fun), [])

    Enum.reduce(forms, {env, acc}, fn form, {e, a} ->
      expand_form(form, module, h, e, a, depth + 1)
    end)
  end

  defp expand_form({:__block__, _, forms}, module, h, env, acc, depth),
    do:
      Enum.reduce(forms, {env, acc}, fn form, {e, a} ->
        expand_form(form, module, h, e, a, depth)
      end)

  defp expand_form(_, _module, _h, env, acc, _depth), do: {env, acc}

  @doc """
  The `use` helpers of one parsed file: `%{module => %{funs: %{name =>
  forms}, using: :dispatch | forms | nil}}`, where funs are zero-arity
  `def`s returning a `quote`, and `using` is the `__using__` quote (or
  `:dispatch` for Phoenix's `apply(__MODULE__, which, [])`).
  """
  def helpers(ast), do: helpers(ast, nil, %{})

  defp helpers({:defmodule, _, [mod, body]}, outer, acc) do
    module =
      case mod do
        {:__aliases__, _, parts} ->
          Enum.join(Enum.reject([outer | Enum.map(parts, &part/1)], &is_nil/1), ".")

        other ->
          print(other)
      end

    forms =
      case body do
        [{:do, {:__block__, _, forms}}] -> forms
        [{:do, form}] -> [form]
        _ -> []
      end

    Enum.reduce(forms, acc, fn form, acc ->
      case form do
        {:defmodule, _, _} ->
          helpers(form, module, acc)

        # ExUnit.CaseTemplate: `using do quote do ... end end` is the
        # template's __using__ (decision 20).
        {:using, _, args} when is_list(args) ->
          quoted =
            case args do
              [[{:do, q}]] -> q
              [_opts, [{:do, q}]] -> q
              _ -> nil
            end

          entry = Map.get(acc, module, %{funs: %{}, using: nil})

          case quoted && quote_forms(quoted) do
            nil -> acc
            fs -> Map.put(acc, module, Map.merge(entry, %{using: fs, template: true}))
          end

        {kind, _, [head, [{:do, quoted}]]} when kind in [:def, :defp, :defmacro] ->
          {fun, args} =
            case head do
              {:when, _, [{f, _, a} | _]} -> {f, a}
              {f, _, a} -> {f, a}
            end

          entry = Map.get(acc, module, %{funs: %{}, using: nil})

          entry =
            cond do
              not ident?(fun) ->
                entry

              str(fun) == "__using__" and dispatch?(quoted) ->
                %{entry | using: :dispatch}

              str(fun) == "__using__" ->
                case quote_forms(quoted) do
                  nil -> entry
                  fs -> %{entry | using: fs}
                end

              args in [nil, []] ->
                case quote_forms(quoted) do
                  nil -> entry
                  fs -> %{entry | funs: Map.put(entry.funs, str(fun), fs)}
                end

              true ->
                entry
            end

          Map.put(acc, module, entry)

        _ ->
          acc
      end
    end)
  end

  defp helpers({:__block__, _, forms}, outer, acc),
    do: Enum.reduce(forms, acc, &helpers(&1, outer, &2))

  defp helpers(_, _, acc), do: acc

  defp quote_forms({:quote, _, [[{:do, {:__block__, _, forms}}]]}), do: forms
  defp quote_forms({:quote, _, [[{:do, form}]]}), do: [form]
  defp quote_forms({:quote, _, [_opts, [{:do, {:__block__, _, forms}}]]}), do: forms
  defp quote_forms({:quote, _, [_opts, [{:do, form}]]}), do: [form]
  defp quote_forms({:__block__, _, forms}), do: forms |> List.last() |> quote_forms()
  defp quote_forms(_), do: nil

  defp dispatch?(body) do
    {_, found} =
      Macro.prewalk(body, false, fn
        {:apply, _, [{:__MODULE__, _, _} | _]} = node, _ -> {node, true}
        node, acc -> {node, acc}
      end)

    found
  end

  # --- Phoenix routes -------------------------------------------------------

  @route_verbs ~w(get post put patch delete options head)
  # Router forms that never hold routes.
  @route_opaque ~w(pipeline plug pipe_through def defp defmacro defmacrop defmodule quote)

  # A Phoenix router: `use Phoenix.Router` or `use <App>Web, :router` at the
  # top of the module body.
  defp router?(body) do
    forms =
      case body do
        [do: {:__block__, _, forms}] -> forms
        [do: form] -> [form]
        _ -> []
      end

    Enum.any?(forms, fn
      {:use, _, [{:__aliases__, _, [p, r]} | _]} when ident?(p) and ident?(r) ->
        str(p) == "Phoenix" and str(r) == "Router"

      {:use, _, [_, router]} when ident?(router) ->
        str(router) == "router"

      _ ->
        false
    end)
  end

  # Walks a router body for verb routes inside nested scopes. `scope` joins
  # its literal path to the enclosing one and appends its alias (expanded
  # through the lexical aliases, as Phoenix does) to the enclosing alias.
  # Routes the walker cannot name (a non-literal path, `live`, `forward`,
  # `resources`, any other macro whose first argument is a "/" path) are
  # recorded in `route_skips` and only counted.
  defp route_walk({:__block__, _, forms}, env, scope, acc), do: route_walk(forms, env, scope, acc)

  defp route_walk(forms, env, scope, acc) when is_list(forms) do
    {_, acc} =
      Enum.reduce(forms, {env, acc}, fn form, {env, acc} -> route_form(form, env, scope, acc) end)

    acc
  end

  defp route_walk(form, env, scope, acc), do: elem(route_form(form, env, scope, acc), 1)

  defp route_form({kind, _, _} = form, env, _scope, acc)
       when kind in [:alias, :require, :import] do
    {env, _} = walk(form, env, new())
    {env, acc}
  end

  defp route_form({fun, meta, args} = form, env, scope, acc) when ident?(fun) and is_list(args) do
    case {str(fun), args} do
      {"scope", _} ->
        {env, scope_form(form, env, scope, acc)}

      {opaque, _} when opaque in @route_opaque ->
        {env, acc}

      {verb, [path, plug, action | rest]} when verb in @route_verbs and length(rest) <= 1 ->
        {env, verb_route(verb, path, plug, action, meta, env, scope, acc)}

      {verb, _} when verb in @route_verbs ->
        {env, route_skip(acc, env, meta, "#{verb} with an unexpected shape")}

      # match :*, "/any", C, :a (and match [:get, :post], ...): its verb
      # comes first, so the "/" path check below would miss it.
      {"match", [_verb, path | _]} when is_binary(path) ->
        {env, route_skip(acc, env, meta, "match")}

      {other, [path | _]} when is_binary(path) ->
        if String.starts_with?(path, "/"),
          do: {env, route_skip(acc, env, meta, other)},
          else: {env, route_blocks(args, env, scope, acc)}

      _ ->
        {env, route_blocks(args, env, scope, acc)}
    end
  end

  defp route_form(_, env, _scope, acc), do: {env, acc}

  # `if`, `live_session` and other blocks: their do/else bodies may hold routes.
  defp route_blocks(args, env, scope, acc) do
    for arg <- args,
        Keyword.keyword?(arg),
        {key, body} <- arg,
        key in [:do, :else],
        reduce: acc do
      acc -> route_walk(body, env, scope, acc)
    end
  end

  defp scope_form({_, meta, args}, env, scope, acc) do
    {positional, blocks} = Enum.split_while(args, &(not do_block?(&1)))

    {path, alias_ast, opts} =
      case positional do
        [path, alias_ast, opts] when is_list(opts) -> {path, alias_ast, opts}
        [path, opts] when is_binary(path) and is_list(opts) -> {path, nil, opts}
        [path, alias_ast] when is_binary(path) -> {path, alias_ast, []}
        [path] when is_binary(path) -> {path, nil, []}
        [opts] when is_list(opts) -> {Keyword.get(opts, :path, "/"), nil, opts}
        other -> {other, nil, []}
      end

    # `alias: false` must survive: `false || nil` would be nil.
    alias_ast =
      if alias_ast != nil,
        do: alias_ast,
        else: if(Keyword.keyword?(opts), do: Keyword.get(opts, :alias))

    inner = %{
      path:
        if(is_binary(path) and scope.path != :unknown,
          do: scope.path ++ segments(path),
          else: :unknown
        ),
      alias:
        cond do
          alias_ast == false ->
            nil

          alias_ast != nil and literal_module?(alias_ast) ->
            join_alias(scope.alias, name(alias_ast, env))

          alias_ast != nil ->
            :unknown

          true ->
            scope.alias
        end
    }

    case blocks do
      [block | _] when is_list(block) ->
        route_walk(Keyword.get(block, :do), env, inner, acc)

      _ ->
        route_skip(acc, env, meta, "scope without a do block")
    end
  end

  defp do_block?(arg),
    do: is_list(arg) and arg != [] and Keyword.keyword?(arg) and Keyword.has_key?(arg, :do)

  defp verb_route(verb, path, plug, action, meta, env, scope, acc) do
    if is_binary(path) and scope.path != :unknown do
      controller =
        cond do
          not literal_module?(plug) or scope.alias == :unknown -> nil
          true -> join_alias(scope.alias, name(plug, env))
        end

      r = %{
        router: env.module,
        method: String.upcase(verb),
        path: "/" <> Enum.join(scope.path ++ segments(path), "/"),
        controller: controller,
        action: if(ident?(action), do: str(action)),
        file: env.file,
        line: line(meta)
      }

      %{acc | routes: [r | acc.routes]}
    else
      route_skip(acc, env, meta, "#{verb} with a non-literal path")
    end
  end

  defp route_skip(acc, env, meta, what),
    do: %{acc | route_skips: [%{file: env.file, line: line(meta), kind: what} | acc.route_skips]}

  defp segments(path), do: String.split(path, "/", trim: true)

  defp join_alias(nil, name), do: name
  defp join_alias(outer, name), do: outer <> "." <> name

  @doc "The module name `ast` denotes under `env` (`:module` and `:aliases`)."
  def module_name(ast, env), do: name(ast, env)

  # apply(Mod, :fun, [a, b]) with literal parts names its target; anything
  # else is a dynamic call the core never resolves.
  defp apply_call(acc, env, [mod, fun, args], meta)
       when ident?(fun) and is_list(args) do
    if literal_module?(mod),
      do: call(acc, env, name(mod, env), fun, length(args), meta, "apply"),
      else: call(acc, env, nil, :apply, 3, meta, "dynamic")
  end

  defp apply_call(acc, env, args, meta),
    do: call(acc, env, nil, :apply, length(args), meta, "dynamic")

  defp call(acc, env, mod, fun, arity, meta, kind, args \\ nil)
  defp call(acc, %{caller: nil}, _, _, _, _, _, _), do: acc

  defp call(acc, env, mod, fun, arity, meta, kind, args) do
    c = %{
      args: args,
      from: env.caller,
      caller_module: env.module,
      module: mod,
      name: str(fun),
      arity: arity,
      line: line(meta),
      kind: kind,
      imports: env.imports,
      uses: Map.get(env, :uses, []),
      file: env.file
    }

    %{acc | calls: [c | acc.calls]}
  end

  # The effective import of one module, as Elixir's elixir_import computes
  # it: `:unknown` (options are not literal; blocks attribution) or
  # `%{functions: spec | nil, macros: spec | nil}`, where nil imports nothing
  # of that kind and a spec is `%{base: :all | :sigils | {:only, set},
  # except: set}` over `{name, arity}` (see `imports?/3`).
  #
  # `only: [...]` keeps the listed names of both kinds; `only: :functions`
  # / `:macros` import every name of that kind and nothing of the other;
  # `only: :sigils` imports sigil_* of both. `except: [...]` (alone or with
  # `only: :functions | :macros | :sigils`) removes names from the previous
  # import of that kind in this scope, or, when there is none, from every
  # name of that kind. Kernel's implicit import is `kernel_import/0`.
  defp import_entry(opts, prior) do
    with true <- is_list(opts) and Enum.all?(opts, &match?({k, _} when ident?(k), &1)),
         {:ok, except} <- except_set(opts[:except]) do
      only = opts[:only]

      calc = fn kind, base ->
        cond do
          except == nil ->
            %{base: base, except: MapSet.new()}

          prior == :unknown ->
            :unknown

          is_map(prior) and prior[kind] != nil ->
            Map.update!(prior[kind], :except, &MapSet.union(&1, except))

          true ->
            %{base: base, except: except}
        end
      end

      entry =
        cond do
          is_list(only) and except == nil ->
            case fun_set(only) do
              {:ok, set} ->
                %{
                  functions: %{base: {:only, set}, except: MapSet.new()},
                  macros: %{base: {:only, set}, except: MapSet.new()}
                }

              :error ->
                :unknown
            end

          only == :functions ->
            %{functions: calc.(:functions, :all), macros: nil}

          only == :macros ->
            %{functions: nil, macros: calc.(:macros, :all)}

          only == :sigils ->
            %{functions: calc.(:functions, :sigils), macros: calc.(:macros, :sigils)}

          only == nil ->
            %{functions: calc.(:functions, :all), macros: calc.(:macros, :all)}

          true ->
            :unknown
        end

      if is_map(entry) and :unknown in Map.values(entry), do: :unknown, else: entry
    else
      _ -> :unknown
    end
  end

  @doc "Kernel's implicit import: every public function and macro."
  def kernel_import,
    do: %{
      functions: %{base: :all, except: MapSet.new()},
      macros: %{base: :all, except: MapSet.new()}
    }

  @doc """
  Whether the effective import `entry` of a module (see `import_entry/2`)
  brings in `name/arity`, which that module defines as `kind` (`:functions`
  or `:macros`).
  """
  def imports?(entry, kind, {name, arity}) when is_map(entry) do
    case entry[kind] do
      nil ->
        false

      %{base: base, except: except} ->
        admitted =
          case base do
            :all -> not String.starts_with?(name, "_")
            :sigils -> String.starts_with?(name, "sigil_")
            {:only, set} -> MapSet.member?(set, {name, arity})
          end

        admitted and not MapSet.member?(except, {name, arity})
    end
  end

  def imports?(_, _, _), do: false

  defp except_set(nil), do: {:ok, nil}
  defp except_set(list) when is_list(list), do: fun_set(list)
  defp except_set(_), do: :error

  defp fun_set(list) do
    if Enum.all?(list, &match?({n, a} when ident?(n) and is_integer(a), &1)),
      do: {:ok, MapSet.new(list, fn {n, a} -> {str(n), a} end)},
      else: :error
  end

  @doc "Last source line covered by an AST node (do/end, closing, end of expression)."
  def max_line(ast) do
    {_, max} =
      Macro.prewalk(ast, 0, fn
        {_, meta, _} = node, acc when is_list(meta) ->
          lines =
            [
              meta
              | for(k <- [:end, :closing, :end_of_expression], m = kw(meta, k), is_list(m), do: m)
            ]
            |> Enum.map(&kw(&1, :line))
            |> Enum.filter(&is_integer/1)

          {node, Enum.max([acc | lines])}

        node, acc ->
          {node, acc}
      end)

    max
  end

  defp kw(list, key) do
    case List.keyfind(list, key, 0) do
      {_, v} -> v
      nil -> nil
    end
  end

  @doc """
  Normalized form of one clause for fingerprints: a structural rendering of
  the AST with all metadata (lines, columns, newlines, delimiters) dropped,
  so whitespace and comment edits do not change it. Atoms and encoded names
  render alike, so it does not depend on which atoms exist.
  """
  def normalize(ast), do: ast |> serialize() |> IO.iodata_to_binary()

  defp serialize({a, meta, b}) when is_list(meta), do: ["(", serialize(a), " ", serialize(b), ")"]
  defp serialize({a, b}), do: ["{", serialize(a), " ", serialize(b), "}"]
  defp serialize(list) when is_list(list), do: ["[", serialize_list(list), "]"]
  defp serialize(name) when ident?(name), do: [":", inspect(str(name))]
  defp serialize(other), do: inspect(other, limit: :infinity, printable_limit: :infinity)

  defp serialize_list([]), do: []
  defp serialize_list([last]), do: serialize(last)
  defp serialize_list([h | t]) when is_list(t), do: [serialize(h), " " | serialize_list(t)]
  defp serialize_list([h | t]), do: [serialize(h), " | ", serialize(t)]

  @doc """
  Readable, deterministic rendering of AST for module names and notes. It
  never needs atoms, so it works on encoded names (unlike Macro.to_string/1).
  """
  def print({:__aliases__, _, parts}) when is_list(parts), do: Enum.map_join(parts, ".", &part/1)

  def print({{:., _, [left, right]}, _, args}) when ident?(right) and is_list(args),
    do: print(left) <> "." <> str(right) <> print_args(args)

  def print({name, _, ctx}) when ident?(name) and is_atom(ctx), do: str(name)

  def print({name, _, args}) when ident?(name) and is_list(args),
    do: str(name) <> print_args(args)

  def print({fun, _, args}) when is_list(args), do: print(fun) <> print_args(args)
  def print({a, b}), do: "{" <> print(a) <> ", " <> print(b) <> "}"
  def print(list) when is_list(list), do: "[" <> Enum.map_join(list, ", ", &print/1) <> "]"

  def print(name) when ident?(name) do
    case str(name) do
      "Elixir." <> rest -> rest
      text when text in ["nil", "true", "false"] -> text
      text -> ":" <> text
    end
  end

  def print(other), do: inspect(other)

  defp print_args(args), do: "(" <> Enum.map_join(args, ", ", &print/1) <> ")"
end
