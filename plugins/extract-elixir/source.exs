defmodule Cartographer.ElixirSource do
  @moduledoc """
  Source-only Elixir AST walker. Parses files with `Code.string_to_quoted/2`
  and never compiles or executes project code.

  It records modules, one entry per function clause, raw call sites (with the
  lexical aliases and imports in scope), Ecto `schema "table"` declarations
  and notes. Call attribution that needs every definition in the repository
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

  def new, do: %{modules: [], clauses: [], calls: [], schemas: [], notes: []}

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
      parse_source(acc, source, relative, repo)
    end
  end

  defp parse_source(acc, source, relative, repo) do
    env = %{
      module: nil,
      aliases: %{},
      imports: %{},
      file: relative,
      repo: repo,
      caller: nil
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
        elem(walk(ast, env, acc), 1)

      {:error, reason} ->
        note(acc, "parse_error", relative, parse_message(reason))
    end
  end

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

    {_, acc} = walk(body, next, acc)
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
          normalized: normalize(clause)
        }

        acc = %{acc | clauses: [c | acc.clauses]}
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

  defp walk({:|>, _, [left, right]}, env, acc) do
    {_, acc} = walk(left, env, acc)

    case right do
      {{:., dotmeta, [mod, fun]}, meta, args} when is_list(args) ->
        walk({{:., dotmeta, [mod, fun]}, meta, [{:__pipe_arg__, [], nil} | args]}, env, acc)

      {fun, meta, args} when ident?(fun) and is_list(args) ->
        walk({fun, meta, [{:__pipe_arg__, [], nil} | args]}, env, acc)

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
          call(acc, env, name(mod, env), fun, length(args), meta, "remote")

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
         do: call(acc, env, nil, fun, arity, meta, "local_or_import"),
         else: acc

    {_, acc} = walk(args, env, acc)
    {env, acc}
  end

  defp walk(items, env, acc) when is_list(items),
    do: Enum.reduce(items, {env, acc}, fn item, {e, a} -> walk(item, e, a) end)

  defp walk(tuple, env, acc) when is_tuple(tuple), do: walk(Tuple.to_list(tuple), env, acc)
  defp walk(_, env, acc), do: {env, acc}

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

  defp call(acc, %{caller: nil}, _, _, _, _, _), do: acc

  defp call(acc, env, mod, fun, arity, meta, kind) do
    c = %{
      from: env.caller,
      caller_module: env.module,
      module: mod,
      name: str(fun),
      arity: arity,
      line: line(meta),
      kind: kind,
      imports: env.imports,
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
