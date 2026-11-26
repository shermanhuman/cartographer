defmodule Cartographer.ElixirDeps do
  @moduledoc """
  Evidence for library calls (map-coverage-2): what a project's locked Hex
  dependencies and the running Elixir/OTP define. Nothing is fetched,
  installed, compiled or evaluated.

  - Each source file takes the nearest `mix.lock` above it, inside the repo
    root. The lock is parsed as data. A `:hex` entry counts only when the
    Hex cache holds `name-version.tar` and its SHA-256 equals the lock's
    outer checksum; otherwise it gives no evidence and a `dependency_missing`
    note names it.
  - A verified package's `lib/**/*.ex` is read in memory, one package at a
    time: each module's public functions and macros (with arity ranges, a
    top-level `for` over literal atoms expanded), and the superset facts of
    its quotes: every definition in any quote (skipping nested modules), the
    modules its quotes `use`, `import` or name in `@before_compile`, and its
    own top-level `use`s. Erlang packages give their `-export` lists.
  - The runtime: every module on the code path (`:code.all_available/0`),
    its exports read once it's needed, and the quotes of Elixir's own apps
    from their bundled sources when present.
  """

  @stdlib_apps ~w(elixir ex_unit logger eex mix iex)a

  defstruct locks: %{}, by_file: %{}, notes: [], runtime: nil, stdlib: %{}, cache: nil

  @doc """
  Evidence for `files` (repo-relative) under `root`, reading tarballs from
  `hex_cache` (nil: no dependency evidence, runtime evidence only).
  """
  def load(root, files, hex_cache) do
    lock_dirs = Map.new(files, fn f -> {f, nearest_lock(root, Path.dirname(f))} end)

    locks =
      lock_dirs
      |> Map.values()
      |> Enum.reject(&is_nil/1)
      |> Enum.uniq()
      |> Map.new(fn dir -> {dir, read_lock(root, dir)} end)

    lock_notes = Enum.flat_map(locks, fn {_dir, {_entries, notes}} -> notes end)
    locks = Map.new(locks, fn {dir, {entries, _notes}} -> {dir, entries} end)

    {packages, notes} =
      Enum.reduce(locks, {%{}, []}, fn {dir, entries}, acc ->
        Enum.reduce(entries, acc, fn e, {pkgs, notes} ->
          key = {e.name, e.version}

          cond do
            Map.has_key?(pkgs, key) ->
              {pkgs, notes}

            hex_cache == nil ->
              {Map.put(pkgs, key, nil), notes}

            true ->
              case index_package(hex_cache, e) do
                {:ok, mods} ->
                  {Map.put(pkgs, key, mods), notes}

                {:error, why} ->
                  note = %{
                    kind: "dependency_missing",
                    file: "#{e.name}-#{e.version}",
                    message: "#{Path.join(dir, "mix.lock")}: #{e.name} #{e.version} gives no evidence (#{why})"
                  }

                  {Map.put(pkgs, key, nil), [note | notes]}
              end
          end
        end)
      end)

    # Per lock: module name => facts, from that lock's verified packages.
    lock_modules =
      Map.new(locks, fn {dir, entries} ->
        mods =
          entries
          |> Enum.flat_map(fn e -> Map.get(packages, {e.name, e.version}) || [] end)
          |> Map.new()

        {dir, mods}
      end)

    {stdlib, stdlib_notes} = stdlib_quotes()

    %__MODULE__{
      locks: lock_modules,
      by_file: lock_dirs,
      notes: lock_notes ++ Enum.reverse(notes) ++ stdlib_notes,
      runtime: runtime_modules(),
      stdlib: stdlib,
      cache: :ets.new(:cartographer_deps, [:set, :public])
    }
  end

  @doc "The lock directory a file's evidence comes from, or nil."
  def lock_of(%__MODULE__{by_file: by_file}, file), do: Map.get(by_file, file)

  @doc """
  The exports of `module` (a name as the walker prints it: `Ecto.Query`,
  `:lists`) under the file's lock: `[{name, min, max}]`, or nil when nothing
  is known about the module.
  """
  def exports(%__MODULE__{} = ev, lock, module) do
    cached(ev, {:exports, lock, module}, fn ->
      case get_in(ev.locks, [lock, module]) do
        # Its own definitions, and what its own top-level `use`s generate
        # into it (`use Comeonin` in Pbkdf2).
        %{exports: ex} = m ->
          generated = Enum.flat_map(Map.get(m, :uses, []), &reach(ev, lock, &1, []).defs)
          Enum.uniq(ex ++ generated)

        nil ->
          runtime_exports(ev, module)
      end
    end)
  end

  @doc "Whether `module` defines `name/arity` as a macro (its source, or the runtime's `__info__(:macros)`)."
  def macro?(%__MODULE__{} = ev, lock, module, name, arity) do
    list =
      cached(ev, {:macros, lock, module}, fn ->
        case get_in(ev.locks, [lock, module]) do
          %{macros: m} -> m
          nil -> runtime_macros(ev, module)
        end
      end)

    exported?(list, name, arity)
  end

  @doc "Whether `{name, arity}` is in an export list."
  def exported?(nil, _name, _arity), do: false

  def exported?(list, name, arity),
    do: Enum.any?(list, fn {n, lo, hi} -> n == name and (lo == -1 or (arity >= lo and arity <= hi)) end)

  @doc "Whether the reader has quote facts for `module` (an indexed library or a stdlib source)."
  def has_quotes?(%__MODULE__{} = ev, lock, module), do: quote_facts(ev, lock, module) != nil

  @doc "Whether anything is known about `module` (an index entry or the runtime)."
  def known?(ev, lock, module), do: exports(ev, lock, module) != nil

  @doc """
  The superset reader (decisions 16 and 23): what a `use` of `module`, with
  the module aliases among its options, can put in the using module.
  Returns `%{defs: [{name, min, max}], imports: [module], unknown: [module]}`:
  definitions from any quote of a reached module, the modules those quotes
  import (their exports apply), and the modules the walk reached but knows
  nothing about (a hole, decision 27).
  """
  def reach(%__MODULE__{} = ev, lock, module, opt_aliases) do
    cached(ev, {:reach, lock, module, Enum.sort(opt_aliases)}, fn ->
      {seen, _} = bfs(ev, lock, [module | opt_aliases], MapSet.new())

      facts = Enum.map(seen, &{&1, quote_facts(ev, lock, &1)})
      defs = facts |> Enum.flat_map(fn {_, f} -> (f && f.defs) || [] end) |> Enum.uniq()
      imports = facts |> Enum.flat_map(fn {_, f} -> (f && f.imports) || [] end) |> Enum.uniq()
      unknown = for {m, nil} <- facts, not known?(ev, lock, m), do: m

      %{defs: defs, imports: imports, unknown: unknown}
    end)
  end

  # How many modules one `use` may reach; past it the reader stops (no
  # real library comes close: Phoenix.LiveView reaches 7).
  @reach_limit 200

  defp bfs(_ev, _lock, [], seen), do: {seen, []}

  defp bfs(ev, lock, [m | rest], seen) do
    if MapSet.member?(seen, m) or MapSet.size(seen) > @reach_limit do
      bfs(ev, lock, rest, seen)
    else
      refs =
        case quote_facts(ev, lock, m) do
          %{refs: r} -> r
          nil -> []
        end

      bfs(ev, lock, refs ++ rest, MapSet.put(seen, m))
    end
  end

  defp quote_facts(ev, lock, module) do
    case get_in(ev.locks, [lock, module]) do
      %{quotes: q} -> q
      nil -> Map.get(ev.stdlib, module)
    end
  end

  defp cached(%__MODULE__{cache: t}, key, fun) do
    case :ets.lookup(t, key) do
      [{_, v}] ->
        v

      [] ->
        v = fun.()
        :ets.insert(t, {key, v})
        v
    end
  end

  # --- locks ---------------------------------------------------------------

  defp nearest_lock(root, dir) do
    cond do
      File.regular?(Path.join([root, dir, "mix.lock"])) -> dir
      dir in [".", "/", ""] -> nil
      true -> nearest_lock(root, Path.dirname(dir))
    end
  end

  @doc """
  The `:hex` entries of a lock, parsed as data with no atom created:
  `{[%{name, version, outer}], notes}`. An unreadable lock, an entry from a
  repository other than hexpm, or one without an outer checksum gives a
  `dependency_missing` note: it gives no evidence.
  """
  def read_lock(root, dir) do
    path = Path.join(dir, "mix.lock")

    with {:ok, text} <- File.read(Path.join(root, path)),
         true <- String.valid?(text),
         {:ok, {m, _, entries}} <- safe_quoted(text),
         "%{}" <- s(m) do
      Enum.reduce(entries, {[], []}, fn entry, {ok, notes} ->
        case lock_entry(entry) do
          {:ok, e} -> {[e | ok], notes}
          {:skip, name, why} -> {ok, [missing_note(path, name, why) | notes]}
          :other -> {ok, notes}
        end
      end)
      |> then(fn {ok, notes} -> {Enum.reverse(ok), Enum.reverse(notes)} end)
    else
      _ -> {[], [missing_note(path, "mix.lock", "the lock can't be read")]}
    end
  end

  # `"key": {:hex, :name, "version", inner, managers, deps, "hexpm", outer}`.
  defp lock_entry({_key, {tag, _, [kind, pkg, version | rest]}}) do
    cond do
      s(tag) != "{}" or s(kind) != "hex" ->
        :other

      not (name_like?(pkg) and is_binary(version)) ->
        :other

      not Regex.match?(~r/\A[0-9A-Za-z.+_-]+\z/, version) ->
        {:skip, s(pkg), "version #{inspect(version)} isn't a version"}

      true ->
        case rest do
          [_inner, _managers, _deps, "hexpm", outer | _] when is_binary(outer) ->
            {:ok, %{name: s(pkg), version: version, outer: String.downcase(outer)}}

          [_inner, _managers, _deps, repo | _] when is_binary(repo) ->
            {:skip, "#{s(pkg)}-#{version}", "from the #{repo} repository, which isn't read"}

          _ ->
            {:skip, "#{s(pkg)}-#{version}", "no outer checksum in the lock"}
        end
    end
  end

  defp lock_entry(_), do: :other

  defp missing_note(lock, file, why),
    do: %{kind: "dependency_missing", file: file, message: "#{lock}: #{file} gives no evidence (#{why})"}

  defp safe_quoted(text) do
    Code.string_to_quoted(text, emit_warnings: false, static_atoms_encoder: &encode/2)
  rescue
    _ -> :error
  end

  # --- packages ------------------------------------------------------------

  defp index_package(hex_cache, e) do
    tar = Path.join(hex_cache, "#{e.name}-#{e.version}.tar")

    with {:ok, bin} <- File.read(tar) |> missing(),
         true <- (:crypto.hash(:sha256, bin) |> Base.encode16(case: :lower)) == e.outer || {:error, "checksum differs from the lock"},
         {:ok, outer} <- :erl_tar.extract({:binary, bin}, [:memory]) |> bad(),
         {_, contents} <- List.keyfind(outer, ~c"contents.tar.gz", 0) || {:error, "no contents"},
         {:ok, files} <- :erl_tar.extract({:binary, contents}, [:memory, :compressed]) |> bad() do
      mods =
        Enum.flat_map(files, fn {path, src} ->
          path = to_string(path)

          cond do
            String.starts_with?(path, "lib/") and String.ends_with?(path, ".ex") -> source_modules(src)
            String.starts_with?(path, "src/") and String.ends_with?(path, ".erl") -> erlang_module(path, src)
            true -> []
          end
        end)

      {:ok, mods}
    end
  end

  defp missing({:ok, _} = ok), do: ok
  defp missing(_), do: {:error, "not in the Hex cache"}
  defp bad({:ok, _} = ok), do: ok
  defp bad(_), do: {:error, "unreadable tarball"}

  defp erlang_module(path, src) do
    exports =
      Regex.scan(~r/-export\(\[([^\]]*)\]\)/s, src)
      |> Enum.flat_map(fn [_, list] ->
        for [_, n, a] <- Regex.scan(~r/'?([a-z][A-Za-z0-9_@]*)'?\s*\/\s*(\d+)/, list),
            do: {n, String.to_integer(a), String.to_integer(a)}
      end)

    [{":" <> Path.basename(path, ".erl"), %{exports: exports, quotes: nil}}]
  end

  @doc "Modules defined in one Elixir source: `[{name, %{exports, quotes}}]`."
  def source_modules(src) do
    with true <- String.valid?(src),
         {:ok, ast} <- safe_quoted(src) do
      modules(ast, nil)
    else
      _ -> []
    end
  end

  # Names become strings, never new atoms (the robustness rule).
  defp encode(token, _meta), do: {:ok, %{__atom__: token}}

  defp modules(ast, outer) do
    {_, acc} =
      Macro.prewalk(ast, [], fn
        {kw, _, [{:__aliases__, _, parts}, [{do_key, body}]]}, acc ->
          # A protocol is a module whose defs are its functions.
          if s(kw) in ["defmodule", "defprotocol"] and s(do_key) == "do" do
            name = Enum.join(Enum.reject([outer | Enum.map(parts, &s/1)], &is_nil/1), ".")
            {nil, [{name, module_facts(name, body)} | modules(body, name) ++ acc]}
          else
            {nil, acc}
          end

        n, acc ->
          {n, acc}
      end)

    acc
  end

  defp module_facts(name, body) do
    forms =
      case body do
        {b, _, f} when is_list(f) -> if s(b) == "__block__", do: f, else: [body]
        f -> [f]
      end

    # Literal list attributes (`@http_methods [:get, :post]`), for `for`.
    attrs =
      for {at, _, [{n, _, [list]}]} <- forms, s(at) == "@", is_list(list), into: %{}, do: {s(n), list}

    exports = Enum.flat_map(forms, &top_exports(&1, %{}, attrs))
    macros = Enum.flat_map(forms, &top_exports(&1, %{}, attrs, ["defmacro"]))
    top_uses = for {u, _, [m | _]} <- forms, s(u) == "use", r = ref(m, name), r != nil, do: r

    {_, q} =
      Macro.prewalk(body, %{defs: [], refs: top_uses, imports: []}, fn
        {kw, _, args} = n, acc when is_list(args) ->
          if s(kw) == "quote", do: {n, quote_scan(args, name, acc, %{})}, else: {n, acc}

        n, acc ->
          {n, acc}
      end)

    %{
      exports: Enum.uniq(exports),
      macros: Enum.uniq(macros),
      uses: Enum.uniq(top_uses),
      quotes: %{defs: Enum.uniq(q.defs), refs: Enum.uniq(q.refs), imports: Enum.uniq(q.imports)}
    }
  end

  @defs ~w(def defmacro defdelegate defguard)
  @all_defs ~w(def defp defmacro defmacrop defdelegate defguard defguardp)

  defp top_exports(form, binds, attrs, kinds \\ @defs)

  defp top_exports({kw, _, args}, binds, attrs, kinds) when is_list(args) do
    k = s(kw)

    cond do
      k in kinds and args != [] -> List.wrap(head(hd(args), binds))
      k == "for" -> for_exports(args, attrs, kinds)
      # Definitions under a module-level `if Code.ensure_loaded?(...)`
      # count, in either branch (a superset, decision 16).
      k in ["if", "unless"] -> args |> List.last() |> branches() |> Enum.flat_map(&top_exports(&1, binds, attrs, kinds))
      # defexception generates exception/1 and message/1; defstruct the struct.
      k == "defexception" and "def" in kinds -> [{"exception", 1, 1}, {"message", 1, 1}, {"__struct__", 0, 1}]
      k == "defstruct" and "def" in kinds -> [{"__struct__", 0, 1}]
      true -> []
    end
  end

  defp top_exports(_, _, _, _), do: []

  defp branches(kw) when is_list(kw) do
    Enum.flat_map(kw, fn
      {k, v} -> if s(k) in ["do", "else"], do: block_forms(v), else: []
      _ -> []
    end)
  end

  defp branches(_), do: []

  # `for m <- [:get, :post], do: defmacro unquote(m)(conn, path) ...`
  defp for_exports(args, attrs, kinds) do
    {gens, body} = for_parts(args, attrs)

    Enum.flat_map(gens, fn {v, atoms} ->
      Enum.flat_map(atoms, fn a -> body |> block_forms() |> Enum.flat_map(&top_exports(&1, %{v => a}, attrs, kinds)) end)
    end)
  end

  defp for_parts(args, attrs \\ %{}) do
    gens =
      for {arrow, _, [{v, _, c}, source]} <- args, s(arrow) == "<-", is_atom(c),
          list = literal_list(source, attrs), is_list(list) do
        {s(v), list |> Enum.filter(&name_like?/1) |> Enum.map(&s/1)}
      end

    body =
      case List.last(args) do
        kw when is_list(kw) -> Enum.find_value(kw, fn {k, v} -> if s(k) == "do", do: v end)
        _ -> nil
      end

    {gens, body}
  end

  defp literal_list(list, _) when is_list(list), do: list

  defp literal_list({at, _, [{n, _, c}]}, attrs) when is_atom(c) do
    if s(at) == "@", do: Map.get(attrs, s(n))
  end

  defp literal_list(_, _), do: nil

  defp block_forms({b, _, f} = n) when is_list(f), do: if(s(b) == "__block__", do: f, else: [n])
  defp block_forms(nil), do: []
  defp block_forms(n), do: [n]

  defp quote_scan(ast, modname, acc, binds) do
    {_, acc} =
      Macro.prewalk(ast, acc, fn
        {kw, _, args} = n, acc when is_list(args) ->
          k = s(kw)

          cond do
            k in ["defmodule", "defimpl", "defprotocol"] ->
              {nil, acc}

            k == "for" ->
              {gens, body} = for_parts(args)

              acc =
                Enum.reduce(gens, acc, fn {v, atoms}, a ->
                  Enum.reduce(atoms, a, fn at, a2 -> quote_scan(body, modname, a2, Map.put(binds, v, at)) end)
                end)

              {nil, acc}

            k in @all_defs ->
              case args do
                [head | _] -> {n, (if (h = head(head, binds)) && k in @defs, do: %{acc | defs: [h | acc.defs]}, else: acc)}
                _ -> {n, acc}
              end

            k == "import" ->
              case args do
                # Exports only, never its quotes (decision 23).
                [m | _] -> {n, add(acc, :imports, ref(m, modname))}
                _ -> {n, acc}
              end

            k == "use" ->
              case args do
                [m | opts] -> {n, Enum.reduce([ref(m, modname) | aliases(opts, modname)], acc, &add(&2, :refs, &1))}
                _ -> {n, acc}
              end

            k == "@" ->
              case args do
                [{attr, _, [m]}] -> {n, if(s(attr) == "before_compile", do: add(acc, :refs, before_ref(m, modname)), else: acc)}
                _ -> {n, acc}
              end

            true ->
              {n, acc}
          end

        n, acc ->
          {n, acc}
      end)

    acc
  end

  defp add(acc, _key, nil), do: acc
  defp add(acc, key, v), do: Map.update!(acc, key, &[v | &1])

  defp before_ref({:{}, _, [m | _]}, modname), do: ref(m, modname)
  defp before_ref({m, _f}, modname), do: ref(m, modname)
  defp before_ref(m, modname), do: ref(m, modname)

  @doc false
  def ref({:__aliases__, _, parts}, _), do: Enum.map_join(parts, ".", &s/1)
  def ref({u, _, [{m, _, _}]}, modname), do: if(s(u) == "unquote" and s(m) == "__MODULE__", do: modname)
  def ref({m, _, c}, modname) when is_atom(c), do: if(s(m) == "__MODULE__", do: modname)
  def ref(_, _), do: nil

  defp aliases(opts, modname) do
    {_, found} =
      Macro.prewalk(opts, [], fn
        {:__aliases__, _, _} = a, acc -> {a, [ref(a, modname) | acc]}
        n, acc -> {n, acc}
      end)

    found
  end

  # A def head: `{name, min, max}`, with `unquote(v)` names taken from the
  # literal `for` bindings in `b`; nil when the name isn't readable.
  defp head({w, _, [h | _]} = node, b) do
    cond do
      s(w) == "when" -> head(h, b)
      s(w) == "unquote" -> unquoted(node, b, -1)
      true -> named(node, b)
    end
  end

  defp head(node, b), do: named(node, b)

  defp named({{u, _, [_]} = un, _, args}, b) when is_list(args) do
    if s(u) == "unquote" do
      case unquoted(un, b, 0) do
        {n, _, _} -> arity_range(n, args)
        nil -> nil
      end
    end
  end

  defp named({name, _, args}, _) when is_list(args) do
    if name_like?(name) and s(name) not in ["unquote", nil], do: arity_range(s(name), args)
  end

  defp named({name, _, c}, _) when is_atom(c) do
    if name_like?(name) and s(name) != "unquote", do: {s(name), 0, 0}
  end

  defp named(_, _), do: nil

  defp unquoted({_, _, [{v, _, c}]}, b, arity) when is_atom(c) do
    case b[s(v)] do
      nil -> nil
      a -> {a, arity, arity}
    end
  end

  defp unquoted(_, _, _), do: nil

  defp arity_range(name, args) do
    d = Enum.count(args, &match?({dd, _, _} when dd == :\\ or dd == %{__atom__: "\\\\"}, &1))
    {name, length(args) - d, length(args)}
  end

  defp name_like?(a) when is_atom(a), do: true
  defp name_like?(%{__atom__: _}), do: true
  defp name_like?(_), do: false

  defp s(%{__atom__: n}), do: n
  defp s(a) when is_atom(a), do: Atom.to_string(a)
  defp s(_), do: nil

  # --- runtime -------------------------------------------------------------

  # Every module on the code path, by the name the walker prints:
  # `Enum` for Elixir.Enum, `:lists` for lists. No atom is created.
  defp runtime_modules do
    for {name, _file, _loaded} <- :code.all_available(), into: %{} do
      n = to_string(name)

      if String.starts_with?(n, "Elixir."),
        do: {String.replace_prefix(n, "Elixir.", ""), n},
        else: {":" <> n, n}
    end
  end

  defp runtime_exports(ev, module) do
    case Map.get(ev.runtime, module) do
      nil ->
        nil

      erl_name ->
        mod = String.to_atom(erl_name)

        if Code.ensure_loaded?(mod) do
          list =
            if function_exported?(mod, :__info__, 1),
              do: mod.__info__(:functions) ++ mod.__info__(:macros),
              else: mod.module_info(:exports)

          for {n, a} <- list, do: {Atom.to_string(n), a, a}
        else
          nil
        end
    end
  end

  defp runtime_macros(ev, module) do
    case Map.get(ev.runtime, module) do
      nil ->
        nil

      erl_name ->
        mod = String.to_atom(erl_name)

        if Code.ensure_loaded?(mod) and function_exported?(mod, :__info__, 1),
          do: for({n, a} <- mod.__info__(:macros), do: {Atom.to_string(n), a, a}),
          else: nil
    end
  end

  # The quotes of Elixir's own apps, from the sources bundled with the
  # runtime (`<lib dir>/lib/**/*.ex`), for `use ExUnit.Case` and the like.
  defp stdlib_quotes do
    Enum.reduce(@stdlib_apps, {%{}, []}, fn app, {acc, notes} ->
      dir = :code.lib_dir(app)
      src = if is_list(dir), do: Path.join(to_string(dir), "lib"), else: nil

      files = if src && File.dir?(src), do: Path.wildcard(Path.join(src, "**/*.ex")), else: []

      if files == [] do
        note = %{kind: "stdlib_sources_missing", file: to_string(app), message: "no bundled sources for #{app}: its use quotes give no evidence"}
        {acc, if(app in [:ex_unit, :elixir], do: [note | notes], else: notes)}
      else
        mods =
          for f <- files, {m, facts} <- source_modules(File.read!(f)), into: %{}, do: {m, facts.quotes}

        {Map.merge(acc, mods), notes}
      end
    end)
  end
end
