defmodule Cartographer.ElixirFacts do
  @moduledoc """
  Builds one facts v3 document for an Elixir repository from the raw output
  of `Cartographer.ElixirSource`: v1's sections plus Phoenix `routes`,
  `http_refs` through Req wrappers (`Cartographer.ElixirHTTP`), `stats`,
  and the templates' LiveView `events`, `event_handlers` and `hook_mounts`.
  See docs/facts-format.md.
  """

  alias Cartographer.{ElixirHTTP, ElixirSource}

  @version "0.3.0"
  @public ~w(def defmacro defdelegate defguard)
  # defguard defines a macro; defdelegate a function.
  @macro_kinds ~w(defmacro defmacrop defguard defguardp)
  @kernel Map.new(
            Enum.map(Kernel.__info__(:functions), &{&1, :functions}) ++
              Enum.map(Kernel.__info__(:macros), &{&1, :macros}),
            fn {{n, a}, kind} -> {{Atom.to_string(n), a}, kind} end
          )
  @ref_extensions ~w(.ex .exs .sql .sh)

  @doc """
  Options: `:repo`, `:root` (required), `:revision`, `:source_roots`
  (default `["lib"]`), `:ref_roots` (default lib, priv/repo/migrations,
  scripts) and `:schema_facts` (path of a schema facts document; enables
  `table_refs`).
  """
  def build(opts), do: opts |> build_with_skips() |> elem(0)

  @doc """
  `build/1`, plus the routes and HTTP references counted as not extracted:
  `{doc, [%{file, line, reason}]}`, sorted.
  """
  def build_with_skips(opts) do
    repo = Keyword.fetch!(opts, :repo)
    root = opts |> Keyword.fetch!(:root) |> Path.expand()
    source_roots = Keyword.get(opts, :source_roots) || ["lib"]
    ref_roots = Keyword.get(opts, :ref_roots) || ["lib", "priv/repo/migrations", "scripts"]

    {sources, skipped} = list_files(root, source_roots, ~w(.ex .exs))
    {heex_files, _} = list_files(root, source_roots, ~w(.heex))
    helpers = use_helpers(root, sources)
    parse = fn acc, text, rel -> ElixirSource.parse(acc, text, rel, repo, helpers: helpers) end

    parsed =
      Enum.reduce(sources, ElixirSource.new(), fn rel, acc ->
        case File.read(Path.join(root, rel)) do
          {:ok, text} ->
            if String.valid?(text) do
              parse.(acc, text, rel)
            else
              # The tokenizer rejects invalid UTF-8 outright; parse the
              # repaired text so the file still contributes its definitions.
              acc
              |> Map.update!(
                :notes,
                &[
                  %{
                    kind: "invalid_utf8",
                    file: rel,
                    message: "invalid UTF-8 replaced with U+FFFD"
                  }
                  | &1
                ]
              )
              |> parse.(String.replace_invalid(text), rel)
            end

          {:error, reason} ->
            %{
              acc
              | notes: [%{kind: "read_error", file: rel, message: inspect(reason)} | acc.notes]
            }
        end
      end)

    parsed = heex_templates(parsed, root, heex_files, repo, helpers)

    {modules, module_notes} = modules(parsed.modules, repo)
    {functions, clauses, function_notes} = functions(parsed.clauses, repo)
    {calls, classified, defined, marked} = calls(parsed, functions, helpers)
    {routes, route_notes} = routes(parsed.routes, functions, repo)
    {http_refs, http_skipped} = ElixirHTTP.refs(parsed.clauses, classified, functions, parsed.attrs)
    browser = events(parsed, classified, functions, repo)

    stats =
      marked
      |> call_stats(defined, MapSet.new(modules, & &1.name))
      |> Map.merge(%{
        language: "elixir",
        dispatch: 0,
        refs: 0,
        not_extracted: length(parsed.route_skips) + length(http_skipped)
      })

    {ref_files, refs, ref_notes} =
      case opts[:schema_facts] do
        nil -> {[], nil, []}
        path -> table_refs(root, ref_roots, path, parsed.schemas, clauses)
      end

    files =
      (sources ++ heex_files ++ ref_files)
      |> Enum.uniq()
      |> Enum.sort()
      |> Enum.map(&file_entry/1)

    notes =
      (parsed.notes ++ skipped ++ module_notes ++ function_notes ++ ref_notes ++ route_notes)
      |> Enum.uniq()
      |> Enum.sort_by(&{&1[:file] || "", &1.kind, &1.message})

    %{
      facts_version: 3,
      producer: %{name: "extract-elixir", version: @version, call_precision: "precise"},
      repo: repo,
      revision: opts[:revision],
      files: files,
      modules: modules,
      functions: functions,
      calls: calls,
      table_refs: refs,
      notes: notes,
      routes: if(routes != [], do: routes),
      http_refs: if(http_refs != [], do: http_refs),
      stats: [stats | browser.stats],
      events: if(browser.events != [], do: browser.events),
      event_handlers: if(browser.handlers != [], do: browser.handlers),
      hook_mounts: if(browser.mounts != [], do: browser.mounts)
    }
    |> Enum.reject(fn {_, v} -> is_nil(v) end)
    |> Map.new()
    |> then(fn doc ->
      skips =
        for(s <- parsed.route_skips, do: %{file: s.file, line: s.line, reason: s.kind}) ++
          Enum.map(http_skipped, &Map.take(&1, [:file, :line, :reason]))

      {doc, Enum.sort(skips)}
    end)
  end

  # Keys in this order first, then alphabetically, so output is stable and
  # reads like docs/facts-format.md regardless of atom table order.
  @key_order ~w(facts_version producer repo revision files modules functions calls table_refs
                notes routes http_refs stats events event_handlers hook_mounts id path from target
                table name version call_precision module arity kind)

  @doc "Encodes `term` as JSON with deterministic key order."
  def encode(term), do: term |> encode_iodata() |> IO.iodata_to_binary()

  defp encode_iodata(map) when is_map(map) do
    pairs =
      map
      |> Enum.map(fn {k, v} -> {to_string(k), v} end)
      |> Enum.sort_by(fn {k, _} ->
        {Enum.find_index(@key_order, &(&1 == k)) || length(@key_order), k}
      end)
      |> Enum.map(fn {k, v} -> [encode_iodata(k), ?:, encode_iodata(v)] end)

    [?{, Enum.intersperse(pairs, ?,), ?}]
  end

  defp encode_iodata(list) when is_list(list),
    do: [?[, list |> Enum.map(&encode_iodata/1) |> Enum.intersperse(?,), ?]]

  # Every string reaching JSON is valid UTF-8: file names, notes and snippets
  # come from repository bytes, and one stray byte must not lose the document.
  defp encode_iodata(text) when is_binary(text), do: JSON.encode!(String.replace_invalid(text))
  defp encode_iodata(other), do: JSON.encode!(other)

  # --- files -----------------------------------------------------------------

  @doc """
  Regular files under `roots` with one of `extensions`. Directories are read
  with `File.ls/1`, not globbed, so glob characters in a path are literal.
  Symlinks (files or directories) are never followed: each one gets a
  `symlink_skipped` note. Dot-files and dot-directories are skipped. A root
  with no matching files gets an `empty_root` note.
  """
  def list_files(root, roots, extensions) do
    {files, notes} =
      Enum.reduce(roots, {[], []}, fn r, {files, notes} ->
        rel = r |> Path.split() |> Enum.reject(&(&1 == ".")) |> join()

        {found, found_notes} =
          case path_type(root, rel) do
            :missing -> {[], []}
            :symlink -> {[], [symlink_note(rel)]}
            _ -> walk_files(root, rel, extensions)
          end

        empty =
          if found == [] and found_notes == [],
            do: [
              %{
                kind: "empty_root",
                message:
                  "#{r} has no #{Enum.join(extensions, "/")} files" <>
                    if(path_type(root, rel) == :missing, do: " (it does not exist)", else: "")
              }
            ],
            else: []

        {found ++ files, empty ++ found_notes ++ notes}
      end)

    {files |> Enum.uniq() |> Enum.sort(), notes |> Enum.uniq() |> Enum.reverse()}
  end

  defp join([]), do: ""
  defp join(parts), do: Path.join(parts)

  defp walk_files(root, rel, extensions) do
    abs = if rel == "", do: root, else: Path.join(root, rel)

    case File.lstat(abs) do
      {:ok, %{type: :symlink}} ->
        {[], [symlink_note(rel)]}

      {:ok, %{type: :directory}} ->
        case File.ls(abs) do
          {:ok, names} ->
            names
            |> Enum.reject(&String.starts_with?(&1, "."))
            |> Enum.sort()
            |> Enum.reduce({[], []}, fn name, {files, notes} ->
              {f, n} = walk_files(root, join([rel, name] |> Enum.reject(&(&1 == ""))), extensions)
              {f ++ files, n ++ notes}
            end)

          {:error, reason} ->
            {[],
             [
               %{
                 kind: "read_error",
                 file: rel,
                 message: "cannot list directory: #{inspect(reason)}"
               }
             ]}
        end

      {:ok, %{type: :regular}} ->
        if Path.extname(rel) in extensions, do: {[rel], []}, else: {[], []}

      _ ->
        {[], []}
    end
  end

  defp symlink_note(rel),
    do: %{kind: "symlink_skipped", file: rel, message: "symlink is not followed"}

  # :symlink if any component from root to `rel` is a symlink.
  defp path_type(_root, ""), do: :directory

  defp path_type(root, rel) do
    rel
    |> Path.split()
    |> Enum.scan(&Path.join(&2, &1))
    |> Enum.reduce_while(nil, fn part, _ ->
      case File.lstat(Path.join(root, part)) do
        {:ok, %{type: :symlink}} -> {:halt, :symlink}
        {:ok, %{type: type}} -> {:cont, type}
        _ -> {:halt, :missing}
      end
    end)
  end

  defp file_entry(path) do
    %{path: path, language: language(path), category: category(path)}
  end

  defp language(path) do
    case Path.extname(path) do
      ext when ext in [".ex", ".exs"] -> "elixir"
      ".heex" -> "heex"
      ".sql" -> "sql"
      ".sh" -> "shell"
      _ -> "text"
    end
  end

  defp category(path) do
    cond do
      String.starts_with?(path, "test/") -> "test"
      String.starts_with?(path, "priv/repo/migrations/") -> "migration"
      String.starts_with?(path, "scripts/") -> "script"
      true -> "source"
    end
  end

  # --- modules and functions -------------------------------------------------

  defp modules(raw, repo) do
    raw
    |> Enum.group_by(& &1.name)
    |> Enum.map(fn {name, defs} ->
      [first | rest] = Enum.sort_by(defs, &{&1.file, &1.line})

      notes =
        for d <- rest,
            do: %{
              kind: "duplicate_module",
              file: d.file,
              message: "line #{d.line}: #{name} is also defined in #{first.file}:#{first.line}"
            }

      {%{
         id: "#{repo}:#{name}",
         name: name,
         namespace: String.split(name, "."),
         file: first.file,
         line: first.line,
         language: "elixir"
       }, notes}
    end)
    |> Enum.sort_by(&elem(&1, 0).id)
    |> then(&{Enum.map(&1, fn {m, _} -> m end), Enum.flat_map(&1, fn {_, n} -> n end)})
  end

  # Clauses with the same id merge into one function. Clauses of the same
  # id in a second file (conditional compilation, copied modules) are noted
  # and left out so `clauses` always refers to `file`.
  defp functions(raw, repo) do
    merged =
      raw
      |> Enum.group_by(& &1.id)
      |> Enum.map(fn {id, cs} ->
        cs = Enum.sort_by(cs, &{&1.file, &1.line})
        first = hd(cs)
        {primary, others} = Enum.split_with(cs, &(&1.file == first.file))

        notes =
          for c <- others,
              do: %{
                kind: "duplicate_definition",
                file: c.file,
                message: "line #{c.line}: #{id} is also defined in #{first.file}"
              }

        f = %{
          id: id,
          module: "#{repo}:#{first.module}",
          name: first.name,
          arity: first.arity,
          min_arity: primary |> Enum.map(& &1.min_arity) |> Enum.min(),
          kind: first.kind,
          file: first.file,
          line: first.line,
          clauses: primary |> Enum.map(& &1.line) |> Enum.uniq() |> Enum.sort(),
          fingerprint: fingerprint(primary)
        }

        {f, primary, notes}
      end)
      |> Enum.sort_by(&elem(&1, 0).id)

    {Enum.map(merged, &elem(&1, 0)), Enum.flat_map(merged, &elem(&1, 1)),
     Enum.flat_map(merged, &elem(&1, 2))}
  end

  @doc "sha256 of the normalized clause sources, in source order."
  def fingerprint(clauses) do
    clauses
    |> Enum.sort_by(& &1.line)
    |> Enum.map_join("\n", & &1.normalized)
    |> then(&:crypto.hash(:sha256, &1))
    |> Base.encode16(case: :lower)
  end

  # --- calls -----------------------------------------------------------------

  # Returns the output calls (without `ext`), the raw calls classified with
  # `:target_module`, `:kind` and `:external` (for the HTTP analysis), the
  # definitions index, and the output calls with `ext` (for stats).
  defp calls(parsed, functions, helpers) do
    raw = parsed.calls

    defined =
      Enum.reduce(functions, %{}, fn f, acc ->
        [_, module] = String.split(f.module, ":", parts: 2)
        kind = if f.kind in @macro_kinds, do: :macros, else: :functions
        entry = {f.min_arity, f.arity, f.kind in @public, kind}
        Map.update(acc, {module, f.name}, [entry], &[entry | &1])
      end)

    ctx = external_context(parsed, defined, helpers)

    classified =
      Enum.map(raw, fn c ->
        {module, kind} = classify(c, defined)
        {module, kind, ext} = external(c, module, kind, ctx)
        %{c | kind: kind} |> Map.put(:target_module, module) |> Map.put(:external, ext)
      end)

    calls =
      classified
      |> Enum.map(fn c ->
        target =
          if c.target_module,
            do: %{module: c.target_module, name: c.name, arity: c.arity},
            else: %{name: c.name, arity: c.arity}

        # A component call keeps its kind even when it's external
        # (decision 21): the HEEx and event analysis reads `component`.
        kind =
          cond do
            Map.get(c, :component, false) and
                c.kind in ~w(local import remote local_or_import external) ->
              "component"

            true ->
              c.kind
          end

        %{from: c.from, target: target, kind: kind, file: c.file, line: c.line, ext: c.external}
      end)
      |> Enum.uniq()
      |> Enum.sort_by(
        &{&1.file, &1.line, &1.from, &1.target[:module] || "", &1.target.name, &1.target.arity,
         &1.kind}
      )

    # The facts document has no field for the outcome: stats read `ext`
    # here, and the emitted calls leave it out.
    {Enum.map(calls, &Map.delete(&1, :ext)), classified, defined, calls}
  end

  # What the external rules need, worked out once per repo.
  defp external_context(parsed, defined, helpers) do
    project = MapSet.new(parsed.modules, & &1.name)

    # Functions a project `use` puts in the using module, as the
    # use-expansion applied them (decision 30): definitions of that module,
    # one per arity a default allows. A name the walk can't read makes the
    # module dynamic.
    quoted = Map.get(parsed, :quoted_defs, [])

    computed =
      quoted |> Enum.filter(&Map.get(&1, :computed, false)) |> MapSet.new(& &1.module)

    defined =
      quoted
      |> Enum.reject(&Map.get(&1, :computed, false))
      |> Enum.reduce(defined, fn d, acc ->
        entry = {d.min, d.max, Map.get(d, :public, true), :functions}
        Map.update(acc, {d.module, d.name}, [entry], &[entry | &1])
      end)

    # Every {name, arity} some project module defines publicly (decision 15).
    public =
      Enum.reduce(defined, MapSet.new(), fn {{_module, name}, entries}, acc ->
        Enum.reduce(entries, acc, fn {min, max, pub, _}, acc ->
          if pub, do: Enum.reduce(min..max, acc, &MapSet.put(&2, {name, &1})), else: acc
        end)
      end)

    # A project helper the walk could expand (its __using__, a CaseTemplate
    # `using` block, or Phoenix's dispatch).
    expandable =
      helpers
      |> Enum.filter(fn {_m, h} -> h.using != nil end)
      |> MapSet.new(fn {m, _} -> m end)

    # Each project module's non-project `use`s (decision 4), and whether it
    # also uses a project module the walk can't expand (decision 21).
    uses =
      Enum.group_by(parsed.uses, & &1.module, & &1.use)
      |> Map.new(fn {m, us} -> {m, Enum.uniq(us)} end)

    # Modules defined in a file with a dynamic_definition note keep their
    # missing functions unresolved (decision 21, P1-12).
    dyn_files =
      parsed.notes
      |> Enum.filter(&(&1.kind == "dynamic_definition"))
      |> MapSet.new(& &1.file)

    dynamic =
      parsed.modules
      |> Enum.filter(&MapSet.member?(dyn_files, &1.file))
      |> MapSet.new(& &1.name)
      |> MapSet.union(computed)

    %{
      project: project,
      public: public,
      expandable: expandable,
      uses: uses,
      dynamic: dynamic,
      defined: defined
    }
  end

  # The library owners of a call's scope: its non-project imports and
  # non-project `use`s (decision 14). `opaque` when the scope uses a project
  # module the walk couldn't expand: then no owner is named (decision 21).
  defp owners(c, ctx) do
    fa = {c.name, c.arity}

    # Only imports that admit the name (`only:`/`except:`, H1-m1); an entry
    # the walk couldn't read admits everything.
    imported =
      for {m, entry} <- c.imports,
          m != "Kernel",
          not is_map(entry) or ElixirSource.imports?(entry, :functions, fa) or
            ElixirSource.imports?(entry, :macros, fa),
          do: m

    used = Map.get(c, :uses, [])

    set =
      (imported ++ used)
      |> Enum.reject(&MapSet.member?(ctx.project, &1))
      |> Enum.uniq()
      |> Enum.sort()

    # A project import whose module uses a library may pass that library's
    # functions on (`import AppWeb.Gettext`): no owner is named then either.
    opaque =
      Enum.any?(used, &(MapSet.member?(ctx.project, &1) and not MapSet.member?(ctx.expandable, &1))) or
        Enum.any?(imported, fn m ->
          MapSet.member?(ctx.project, m) and
            Enum.any?(Map.get(ctx.uses, m, []), &(not MapSet.member?(ctx.project, &1)))
        end)

    {set, opaque}
  end

  # Decisions 2, 4, 14, 15, 21: a call nothing in the project defines goes
  # to a library when its scope imports or uses one. Returns the target
  # module, the kind and whether it's external.
  defp external(c, module, kind, ctx) do
    fa = {c.name, c.arity}

    cond do
      # A bare call (or capture) left unresolved: the scope's library owners.
      # Unless the caller, or a project module it imports, has functions
      # the inventory can't list (H1-M1): then the call may be theirs.
      module == nil and kind in ["local_or_import", "capture"] and
        not MapSet.member?(ctx.public, fa) and not reaches_dynamic?(c, ctx) and
          not defined?(ctx.defined, c.caller_module, c.name, c.arity, false) ->
        case owners(c, ctx) do
          {[], _} -> {nil, kind, false}
          {[owner], false} -> {owner, external_kind(kind), true}
          {_, _} -> {nil, external_kind(kind), true}
        end

      # Mod.f/a to a project module that doesn't define it but uses a
      # library: a function that library generated (`use Ecto.Repo`).
      # Captures and apply/3 of such a function too, keeping their kinds
      # (H1-m2).
      module != nil and kind in ["remote", "capture", "apply"] and MapSet.member?(ctx.project, module) and
          not MapSet.member?(ctx.dynamic, module) ->
        libs =
          ctx.uses
          |> Map.get(module, [])
          |> Enum.reject(&MapSet.member?(ctx.project, &1))

        if libs != [] and not defined?(ctx.defined, module, c.name, c.arity, false),
          do: {module, if(kind == "remote", do: "external", else: kind), true},
          else: {module, kind, false}

      # Already a library module (Enum.map/2): external, as before.
      module != nil and not MapSet.member?(ctx.project, module) ->
        {module, kind, true}

      true ->
        {module, kind, false}
    end
  end

  defp reaches_dynamic?(c, ctx) do
    MapSet.member?(ctx.dynamic, Map.get(c, :caller_module)) or
      Enum.any?(Map.keys(c.imports), &MapSet.member?(ctx.dynamic, &1))
  end

  defp external_kind("capture"), do: "capture"
  defp external_kind(_), do: "external"

  # Call sites by outcome: in-repo when the target names a function in this
  # document, external when it names a module outside it, else unresolved.
  defp call_stats(calls, defined, modules) do
    Enum.reduce(calls, %{call_sites: 0, in_repo: 0, external: 0, unresolved: 0}, fn c, acc ->
      module = c.target[:module]

      outcome =
        cond do
          module && defined?(defined, module, c.target.name, c.target.arity, false) -> :in_repo
          Map.get(c, :ext, false) -> :external
          module && not MapSet.member?(modules, module) -> :external
          true -> :unresolved
        end

      acc |> Map.update!(:call_sites, &(&1 + 1)) |> Map.update!(outcome, &(&1 + 1))
    end)
  end

  # --- routes ----------------------------------------------------------------

  # A route's handler is Controller.action/2 when this document defines it.
  # Ids carry the router module, so two apps in one repo can serve the same
  # path. The same router, method and path a second time (a later scope that
  # Phoenix never reaches) is noted and left out.
  defp routes(raw, functions, repo) do
    ids = MapSet.new(functions, & &1.id)

    raw
    |> Enum.sort_by(&{&1.file, &1.line})
    |> Enum.reduce({[], %{}, []}, fn r, {routes, seen, notes} ->
      id = "#{repo}:route:#{r.router}:#{r.method} #{r.path}"

      case seen do
        %{^id => first} ->
          note = %{
            kind: "duplicate_route",
            file: r.file,
            message:
              "line #{r.line}: #{r.method} #{r.path} is also defined in #{first.file}:#{first.line}"
          }

          {routes, seen, [note | notes]}

        _ ->
          handler = r.controller && r.action && "#{repo}:#{r.controller}.#{r.action}/2"

          route =
            %{id: id, method: r.method, path: r.path, file: r.file, line: r.line}
            |> then(fn route ->
              if handler in ids,
                do: Map.merge(route, %{handler: handler, precision: "precise"}),
                else: route
            end)

          {[route | routes], Map.put(seen, id, r), notes}
      end
    end)
    |> then(fn {routes, _, notes} -> {Enum.sort_by(routes, & &1.id), notes} end)
  end

  # An unqualified call goes to a local definition, else to the one module
  # whose effective import (ElixirSource.import_entry/2: a later import of a
  # module replaces the earlier one, only/except filter by name/arity and by
  # function vs macro) brings it in. Only modules defined in this repo can
  # be checked; imports of other modules are not candidates (as before), and
  # a module whose import options are not literal blocks attribution when it
  # defines the name. Kernel is the fallback when nothing else imports it.
  defp classify(%{module: nil, kind: kind} = c, defined)
       when kind in ["local_or_import", "capture"] do
    as = fn resolved -> if kind == "capture", do: "capture", else: resolved end
    fa = {c.name, c.arity}

    if defined?(defined, c.caller_module, c.name, c.arity, false) do
      {c.caller_module, as.("local")}
    else
      matches =
        for {module, entry} <- Enum.sort(c.imports),
            module != "Kernel",
            def_kind = defined_kind(defined, module, c.name, c.arity),
            def_kind != nil,
            entry == :unknown or ElixirSource.imports?(entry, def_kind, fa),
            uniq: true,
            do: {module, entry}

      kernel = Map.get(c.imports, "Kernel", ElixirSource.kernel_import())

      case matches do
        [{module, entry}] when entry != :unknown ->
          {module, as.("import")}

        [] ->
          case Map.fetch(@kernel, fa) do
            {:ok, kernel_kind} ->
              if ElixirSource.imports?(kernel, kernel_kind, fa),
                do: {"Kernel", as.("import")},
                else: {nil, kind}

            :error ->
              {nil, kind}
          end

        _ ->
          {nil, kind}
      end
    end
  end

  defp classify(c, _), do: {c.module, c.kind}

  defp defined?(defined, module, name, arity, public_only) do
    defined
    |> Map.get({module, name}, [])
    |> Enum.any?(fn {min, max, public, _} ->
      arity in min..max and (public or not public_only)
    end)
  end

  # :functions or :macros for a public definition of module.name/arity in
  # this repo, else nil.
  defp defined_kind(defined, module, name, arity) do
    defined
    |> Map.get({module, name}, [])
    |> Enum.find_value(fn {min, max, public, kind} ->
      if public and arity in min..max, do: kind
    end)
  end

  # --- templates and LiveView events ------------------------------------------

  # The `use` helpers of every source file (see ElixirSource.helpers/1),
  # read before the walk so a module sees its app's helpers wherever the
  # app module is defined.
  defp use_helpers(root, sources) do
    Enum.reduce(sources, %{}, fn rel, acc ->
      with {:ok, text} <- File.read(Path.join(root, rel)),
           true <- String.valid?(text),
           {:ok, ast} <-
             (try do
                Code.string_to_quoted(text,
                  emit_warnings: false,
                  file: rel,
                  static_atoms_encoder: &ElixirSource.encode_atom/2
                )
              rescue
                _ -> :error
              end) do
        Map.merge(acc, ElixirSource.helpers(ast), fn _, a, b ->
          %{
            funs: Map.merge(a.funs, b.funs),
            using: b.using || a.using,
            template: Map.get(b, :template, false) or Map.get(a, :template, false)
          }
        end)
      else
        _ -> acc
      end
    end)
  end

  # .heex files compile into functions: a colocated `name.html.heex` beside
  # `name.ex` is that module's render/1, and a file matched by a module's
  # `embed_templates "dir/*"` is `<Module>.<file name>/1`. Each becomes a
  # function (unless the module defines it) and is read like a ~H body, in
  # the module's scope.
  defp heex_templates(parsed, _root, [], _repo, _helpers), do: parsed

  defp heex_templates(parsed, root, files, repo, helpers) do
    envs = Enum.group_by(parsed.module_envs, & &1.file)
    by_module = Map.new(parsed.module_envs, &{&1.module, &1})
    ids = MapSet.new(parsed.clauses, & &1.id)

    Enum.reduce(files, parsed, fn rel, acc ->
      base = rel |> Path.basename() |> String.replace(~r/(\.[a-z]+)?\.heex$/, "")
      stem = Path.join(Path.dirname(rel), base)

      owner =
        cond do
          (mods = Map.get(envs, stem <> ".ex", [])) != [] ->
            {hd(Enum.sort_by(mods, & &1.module)), "render"}

          (e = embed_owner(acc.embeds, String.replace_suffix(rel, ".heex", ""))) != nil ->
            {Map.get(by_module, e.module), base <> e.suffix}

          true ->
            nil
        end

      case {owner, File.read(Path.join(root, rel))} do
        {{%{} = env0, fun}, {:ok, text}} ->
          fid = "#{repo}:#{env0.module}.#{fun}/1"

          acc =
            if MapSet.member?(ids, fid),
              do: acc,
              else: %{
                acc
                | clauses: [
                    %{
                      id: fid,
                      module: env0.module,
                      name: fun,
                      arity: 1,
                      min_arity: 1,
                      kind: "def",
                      file: rel,
                      line: 1,
                      end_line: length(String.split(text, "\n")),
                      normalized: text
                    }
                    | acc.clauses
                  ]
              }

          env = %{
            module: env0.module,
            aliases: env0.aliases,
            imports: env0.imports,
            # The module's `use`s: a template's library calls get their
            # owners as the module's own code does (decision 14).
            uses: Map.get(env0, :uses, []),
            file: rel,
            repo: repo,
            caller: fid,
            helpers: helpers
          }

          ElixirSource.template(acc, env, text, 1)

        {nil, _} ->
          %{
            acc
            | notes: [
                %{
                  kind: "heex_unowned",
                  file: rel,
                  message: "no module renders this template (not colocated, no embed_templates)"
                }
                | acc.notes
              ]
          }

        {_, {:error, reason}} ->
          %{acc | notes: [%{kind: "read_error", file: rel, message: inspect(reason)} | acc.notes]}
      end
    end)
  end

  defp embed_owner(embeds, stem) do
    Enum.find_value(embeds, fn e ->
      # Matched against the path without .heex ("mail/welcome.html"), as
      # Phoenix does: "mail/*.html" takes only html templates, and a pattern
      # with no format ("mail/*") takes every format.
      pattern = Path.join(Path.dirname(e.file), e.pattern)

      regex =
        pattern
        |> Regex.escape()
        |> String.replace("\\*\\*", ".*")
        |> String.replace("\\*", "[^/]*")

      if Regex.match?(~r/\A#{regex}\z/, stem), do: e
    end)
  end

  # The module kind a `use` makes: a LiveView, a LiveComponent or a
  # function-component module, directly or through an in-repo helper.
  defp module_kinds(uses) do
    Enum.reduce(uses, %{}, fn u, acc ->
      kind =
        cond do
          u.use == "Phoenix.LiveView" or u.kind == "live_view" -> :live_view
          u.use == "Phoenix.LiveComponent" or u.kind == "live_component" -> :live_component
          u.use == "Phoenix.Component" or u.kind in ["html", "component"] -> :component
          true -> nil
        end

      rank = %{live_view: 3, live_component: 2, component: 1}

      if kind && Map.get(rank, kind) > Map.get(rank, Map.get(acc, u.module), 0),
        do: Map.put(acc, u.module, kind),
        else: acc
    end)
  end

  @doc false
  # Resolves sends to the handle_event functions that receive them:
  #   * a template in a LiveView, or `phx-target={@myself}` in a
  #     LiveComponent, goes to that module (precise);
  #   * anything else goes to the LiveViews that render the sending
  #     function, found up the call graph (heuristic), as LiveView sends an
  #     untargeted event to the LiveView whatever component renders it;
  # and a receiver only counts when it has a clause for the name (its head,
  # a guard or a module attribute), or a catch-all that delegates to a
  # module that has one.
  defp events(parsed, classified, functions, repo) do
    kinds = module_kinds(parsed.uses)
    fids = MapSet.new(functions, & &1.id)

    module_of =
      Map.new(functions, fn f -> {f.id, String.replace_prefix(f.module, repo <> ":", "")} end)

    span =
      Enum.reduce(functions, %{}, fn f, acc ->
        Map.update(
          acc,
          {module_of[f.id], f.name},
          [{f.min_arity, f.arity, f.id}],
          &[{f.min_arity, f.arity, f.id} | &1]
        )
      end)

    resolve = fn module, name, arity ->
      span
      |> Map.get({module, name}, [])
      |> Enum.find_value(fn {min, max, id} -> if arity in min..max, do: id end)
    end

    callers =
      Enum.reduce(classified, %{}, fn c, acc ->
        with m when is_binary(m) <- c.target_module,
             to when is_binary(to) <- resolve.(m, c.name, c.arity),
             true <- c.from != to do
          Map.update(acc, to, MapSet.new([c.from]), &MapSet.put(&1, c.from))
        else
          _ -> acc
        end
      end)

    attrs = Map.new(parsed.attrs, &{{&1.module, &1.name}, &1.values})

    handlers =
      parsed.handlers
      |> Enum.flat_map(fn
        %{source: "attribute"} = h ->
          for e <- Map.get(attrs, {h.module, h.attr}, []),
              e != "",
              do: %{function: h.function, event: e, source: "attribute", line: h.line}

        %{source: "delegate"} = h ->
          d = "#{repo}:#{h.delegate_module}.handle_event/3"

          if MapSet.member?(fids, d),
            do: [%{function: h.function, delegate: d, source: "delegate", line: h.line}],
            else: []

        h ->
          [h]
      end)
      |> Enum.filter(&MapSet.member?(fids, &1.function))
      |> Enum.uniq()
      |> Enum.sort_by(&{&1.function, &1.line, &1[:event] || "", &1[:delegate] || ""})

    names =
      Enum.reduce(handlers, %{}, fn h, acc ->
        if h[:event],
          do: Map.update(acc, h.function, MapSet.new([h.event]), &MapSet.put(&1, h.event)),
          else: acc
      end)

    delegates =
      Enum.reduce(handlers, %{}, fn h, acc ->
        if h[:delegate],
          do: Map.update(acc, h.function, [h.delegate], &[h.delegate | &1]),
          else: acc
      end)

    # Modules of kind `want` that render fid, walking callers up (a LiveView
    # stops the walk).
    up = fn fid, want ->
      Stream.unfold({[fid], MapSet.new([fid]), 0}, fn
        {[], _, _} ->
          nil

        {_, _, depth} when depth > 12 ->
          nil

        {frontier, seen, depth} ->
          found =
            for f <- frontier, kinds[module_of[f]] in want, into: MapSet.new(), do: module_of[f]

          next =
            for f <- frontier,
                kinds[module_of[f]] != :live_view,
                c <- Map.get(callers, f, []),
                not MapSet.member?(seen, c),
                uniq: true,
                do: c

          {found, {next, Enum.into(next, seen), depth + 1}}
      end)
      |> Enum.reduce(MapSet.new(), &MapSet.union/2)
      |> Enum.sort()
    end

    receivers = fn from, target ->
      module = module_of[from]

      cond do
        target == "myself" and kinds[module] == :live_component -> {[module], :precise}
        target == "myself" -> {up.(from, [:live_component]), :heuristic}
        target == "dynamic" -> {[], :heuristic}
        kinds[module] == :live_view -> {[module], :precise}
        true -> {up.(from, [:live_view]), :heuristic}
      end
    end

    # handle_event functions of the receivers that take this name.
    takers = fn modules, event ->
      Enum.flat_map(modules, fn m ->
        he = "#{repo}:#{m}.handle_event/3"

        # One delegation hop: a clause handing the name to another module's
        # handle_event that takes it.
        ds =
          Enum.filter(
            Map.get(delegates, he, []),
            &MapSet.member?(Map.get(names, &1, MapSet.new()), event)
          )

        cond do
          MapSet.member?(Map.get(names, he, MapSet.new()), event) ->
            [{he, :direct} | Enum.map(ds, &{&1, :delegated})]

          ds != [] ->
            [{he, :delegated} | Enum.map(ds, &{&1, :delegated})]

          true ->
            []
        end
      end)
    end

    # A component sending whatever its caller passes (phx-click={@click}):
    # each call site that passes the attribute is a send from the caller,
    # by name for a literal, dynamic for code. A component no call passes
    # it to is a dynamic send of its own.
    passed =
      for p <- parsed.passthrough,
          c <- classified,
          Map.get(c, :component),
          c.target_module,
          resolve.(c.target_module, c.name, c.arity) == p.component,
          {name, value} <- Map.get(c, :attrs, []),
          name == p.assign,
          not (is_binary(value) and String.starts_with?(value, "lv:")),
          do:
            {p,
             %{
               from: c.from,
               module: module_of[c.from],
               via: p.via,
               target: p.target,
               file: c.file,
               line: c.line,
               event: if(is_binary(value) and value != "", do: value),
               dynamic: not (is_binary(value) and value != ""),
               passed: true
             }}

    reached = MapSet.new(passed, fn {p, _} -> {p.component, p.assign} end)
    passthrough = Enum.map(passed, &elem(&1, 1))

    unfilled =
      for p <- parsed.passthrough,
          not MapSet.member?(reached, {p.component, p.assign}),
          do: %{
            from: p.component,
            module: module_of[p.component],
            via: p.via,
            target: p.target,
            file: p.file,
            line: p.line,
            event: nil,
            dynamic: true
          }

    sends =
      (parsed.events ++ passthrough ++ unfilled)
      |> Enum.filter(&MapSet.member?(fids, &1.from))
      |> Enum.map(fn e ->
        if e.dynamic do
          %{from: e.from, dynamic: true, via: e.via, target: e.target, file: e.file, line: e.line}
        else
          {mods, how} = receivers.(e.from, e.target)
          to = takers.(mods, e.event)

          precise =
            how == :precise and not Map.get(e, :passed, false) and
              Enum.all?(to, &(elem(&1, 1) == :direct))

          %{
            from: e.from,
            event: e.event,
            via: e.via,
            target: e.target,
            to: to |> Enum.map(&elem(&1, 0)) |> Enum.uniq() |> Enum.sort(),
            file: e.file,
            line: e.line,
            precision: if(precise, do: "precise", else: "heuristic")
          }
        end
      end)
      |> Enum.map(fn e -> if e[:to] == [], do: Map.delete(e, :to), else: e end)
      |> Enum.map(fn e -> if e.target == "", do: Map.delete(e, :target), else: e end)
      |> Enum.uniq()
      |> Enum.sort_by(&{&1.file, &1.line, &1.from, &1[:event] || "", &1.via})

    mounts =
      parsed.hook_mounts
      |> Enum.filter(&MapSet.member?(fids, &1.from))
      |> Enum.map(fn m ->
        {mods, _} = receivers.(m.from, "")

        rs =
          for mod <- mods, he = "#{repo}:#{mod}.handle_event/3", MapSet.member?(fids, he), do: he

        Map.put(m, :receivers, Enum.sort(rs))
      end)
      |> Enum.map(fn m -> if m.receivers == [], do: Map.delete(m, :receivers), else: m end)
      |> Enum.uniq()
      |> Enum.sort_by(&{&1.file, &1.line, &1.hook})

    stats =
      if sends == [],
        do: [],
        else: [
          %{
            language: "heex",
            call_sites: length(sends),
            in_repo: Enum.count(sends, &(&1[:to] != nil)),
            dispatch: 0,
            external: 0,
            unresolved: Enum.count(sends, &(not Map.get(&1, :dynamic, false) and &1[:to] == nil)),
            refs: 0,
            not_extracted: Enum.count(sends, &Map.get(&1, :dynamic, false))
          }
        ]

    handlers_out =
      Enum.map(handlers, &Map.take(&1, [:function, :event, :source, :delegate, :line]))

    %{events: sends, handlers: handlers_out, mounts: mounts, stats: stats}
  end

  # --- table references ------------------------------------------------------

  defp table_refs(root, ref_roots, schema_path, schemas, clauses) do
    schema = schema_path |> File.read!() |> JSON.decode!()

    if schema["facts_version"] not in [1, 2, 3],
      do:
        raise(ArgumentError, "--schema-facts #{schema_path} is not a facts v1, v2 or v3 document")

    by_name =
      (schema["tables"] || [])
      |> Enum.group_by(& &1["name"], & &1["id"])
      |> Map.new(fn {name, ids} -> {name, Enum.sort(ids)} end)

    {files, skipped} = list_files(root, ref_roots, @ref_extensions)
    lines = Map.new(files, &{&1, read_lines(root, &1)})

    text_refs =
      for {file, text_lines} <- lines,
          {text, line} <- code_lines(file, text_lines),
          word <- ref_words(file, text),
          id <- Map.get(by_name, word, []),
          do: %{table: id, file: file, line: line, text: snippet(text)}

    {schema_refs, notes} =
      Enum.reduce(schemas, {[], []}, fn s, {refs, notes} ->
        case Map.get(by_name, s.table) do
          nil ->
            {refs,
             [
               %{
                 kind: "unknown_table",
                 file: s.file,
                 message:
                   "line #{s.line}: Ecto schema #{s.module} declares table \"#{s.table}\", which is not in the schema facts"
               }
               | notes
             ]}

          ids ->
            text = root |> read_lines(s.file) |> Enum.at(s.line - 1, "")

            {for(id <- ids, do: %{table: id, file: s.file, line: s.line, text: snippet(text)}) ++
               refs, notes}
        end
      end)

    ranges = Enum.group_by(clauses, & &1.file, &{&1.line, &1.end_line, &1.id})

    refs =
      (text_refs ++ schema_refs)
      |> Enum.uniq_by(&{&1.table, &1.file, &1.line})
      |> Enum.map(fn r ->
        case enclosing(Map.get(ranges, r.file, []), r.line) do
          nil -> r
          id -> Map.put(r, :function, id)
        end
      end)
      |> Enum.sort_by(&{&1.file, &1.line, &1.table})

    {files, refs, skipped ++ notes}
  end

  defp read_lines(root, file) do
    case File.read(Path.join(root, file)) do
      {:ok, text} -> text |> String.replace_invalid() |> String.split("\n")
      _ -> []
    end
  end

  # Numbered lines that can hold a table reference: comment lines are
  # skipped everywhere, and in Elixir files so are @moduledoc/@doc/@typedoc/
  # @shortdoc attributes, including the whole of a heredoc.
  @doc_attr ~r/^\s*@(?:moduledoc|doc|typedoc|shortdoc)\b/
  @doc_heredoc ~r/^\s*@(?:moduledoc|doc|typedoc|shortdoc)\s+(?:~[sS])?("""|\'\'\')\s*$/

  defp code_lines(file, lines) do
    elixir = Path.extname(file) in [".ex", ".exs"]

    lines
    |> Enum.with_index(1)
    |> Enum.reduce({[], nil}, fn {text, n}, {kept, closing} ->
      trimmed = String.trim_leading(text)

      cond do
        closing != nil ->
          {kept, if(String.starts_with?(trimmed, closing), do: nil, else: closing)}

        String.starts_with?(trimmed, ["#", "--"]) ->
          {kept, nil}

        elixir and Regex.match?(@doc_attr, text) ->
          case Regex.run(@doc_heredoc, text) do
            [_, delimiter] -> {kept, delimiter}
            nil -> {kept, nil}
          end

        true ->
          {[{text, n} | kept], nil}
      end
    end)
    |> elem(0)
    |> Enum.reverse()
  end

  # In SQL and shell files any word may name a table. In Elixir only forms
  # that name one in code count: an atom (`:t`, `table(:t)`,
  # `references(:t)`), a whole string (`"t"`, `schema "t"`,
  # `from x in "t"`), or a word after a SQL keyword (`FROM t`, `JOIN t`,
  # `INTO t`, `UPDATE t`, `TABLE t`, `REFERENCES t`) as in raw `execute` SQL.
  @word ~r/\b[a-z][a-z0-9_]*\b/
  @atom ~r/(?<![\w:]):([a-z][a-z0-9_]*)\b/
  @string ~r/"(?:[a-z][a-z0-9_]*\.)?([a-z][a-z0-9_]*)"/
  @sql ~r/\b(?i:from|join|into|update|table|references)\s+(?:"?[a-z][a-z0-9_]*"?\.)?"?([a-z][a-z0-9_]*)\b/

  defp ref_words(file, text) do
    if Path.extname(file) in [".ex", ".exs"] do
      [@atom, @string, @sql]
      |> Enum.flat_map(&Regex.scan(&1, text, capture: :all_but_first))
      |> List.flatten()
      |> Enum.uniq()
    else
      @word |> Regex.scan(text) |> List.flatten() |> Enum.uniq()
    end
  end

  defp snippet(text), do: text |> String.trim() |> String.slice(0, 220)

  defp enclosing(ranges, line) do
    ranges
    |> Enum.filter(fn {first, last, _} -> first <= line and line <= last end)
    |> Enum.min_by(fn {first, last, _} -> last - first end, fn -> nil end)
    |> case do
      nil -> nil
      {_, _, id} -> id
    end
  end
end
