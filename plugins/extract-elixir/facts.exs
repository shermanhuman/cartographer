defmodule Cartographer.ElixirFacts do
  @moduledoc """
  Builds one facts v1 document for an Elixir repository from the raw output
  of `Cartographer.ElixirSource`. See docs/facts-format.md.
  """

  alias Cartographer.ElixirSource

  @version "0.1.0"
  @public ~w(def defmacro defdelegate defguard)
  @kernel MapSet.new(
            Kernel.__info__(:functions) ++ Kernel.__info__(:macros),
            fn {n, a} -> {Atom.to_string(n), a} end
          )
  @ref_extensions ~w(.ex .exs .sql .sh)

  @doc """
  Options: `:repo`, `:root` (required), `:revision`, `:source_roots`
  (default `["lib"]`), `:ref_roots` (default lib, priv/repo/migrations,
  scripts) and `:schema_facts` (path of a schema facts document; enables
  `table_refs`).
  """
  def build(opts) do
    repo = Keyword.fetch!(opts, :repo)
    root = opts |> Keyword.fetch!(:root) |> Path.expand()
    source_roots = Keyword.get(opts, :source_roots) || ["lib"]
    ref_roots = Keyword.get(opts, :ref_roots) || ["lib", "priv/repo/migrations", "scripts"]

    {sources, skipped} = list_files(root, source_roots, ~w(.ex .exs))

    parsed =
      Enum.reduce(sources, ElixirSource.new(), fn rel, acc ->
        case File.read(Path.join(root, rel)) do
          {:ok, text} ->
            if String.valid?(text) do
              ElixirSource.parse(acc, text, rel, repo)
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
              |> ElixirSource.parse(String.replace_invalid(text), rel, repo)
            end

          {:error, reason} ->
            %{
              acc
              | notes: [%{kind: "read_error", file: rel, message: inspect(reason)} | acc.notes]
            }
        end
      end)

    {modules, module_notes} = modules(parsed.modules, repo)
    {functions, clauses, function_notes} = functions(parsed.clauses, repo)
    calls = calls(parsed.calls, functions)

    {ref_files, refs, ref_notes} =
      case opts[:schema_facts] do
        nil -> {[], nil, []}
        path -> table_refs(root, ref_roots, path, parsed.schemas, clauses)
      end

    files =
      (sources ++ ref_files)
      |> Enum.uniq()
      |> Enum.sort()
      |> Enum.map(&file_entry/1)

    notes =
      (parsed.notes ++ skipped ++ module_notes ++ function_notes ++ ref_notes)
      |> Enum.uniq()
      |> Enum.sort_by(&{&1[:file] || "", &1.kind, &1.message})

    %{
      facts_version: 1,
      producer: %{name: "extract-elixir", version: @version, call_precision: "precise"},
      repo: repo,
      revision: opts[:revision],
      files: files,
      modules: modules,
      functions: functions,
      calls: calls,
      table_refs: refs,
      notes: notes
    }
    |> Enum.reject(fn {_, v} -> is_nil(v) end)
    |> Map.new()
  end

  # Keys in this order first, then alphabetically, so output is stable and
  # reads like docs/facts-format.md regardless of atom table order.
  @key_order ~w(facts_version producer repo revision files modules functions calls table_refs
                notes id path from target table name version call_precision module arity kind)

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

  defp calls(raw, functions) do
    defined =
      Enum.reduce(functions, %{}, fn f, acc ->
        [_, module] = String.split(f.module, ":", parts: 2)
        entry = {f.min_arity, f.arity, f.kind in @public}
        Map.update(acc, {module, f.name}, [entry], &[entry | &1])
      end)

    raw
    |> Enum.map(fn c ->
      {module, kind} = classify(c, defined)

      target =
        if module,
          do: %{module: module, name: c.name, arity: c.arity},
          else: %{name: c.name, arity: c.arity}

      %{from: c.from, target: target, kind: kind, file: c.file, line: c.line}
    end)
    |> Enum.uniq()
    |> Enum.sort_by(
      &{&1.file, &1.line, &1.from, &1.target[:module] || "", &1.target.name, &1.target.arity,
       &1.kind}
    )
  end

  defp classify(%{module: nil, kind: kind} = c, defined)
       when kind in ["local_or_import", "capture"] do
    as = fn resolved -> if kind == "capture", do: "capture", else: resolved end

    cond do
      defined?(defined, c.caller_module, c.name, c.arity, false) ->
        {c.caller_module, as.("local")}

      true ->
        candidates =
          for {module, filter} <- c.imports,
              module != "Kernel",
              allows?(filter, c.name, c.arity),
              defined?(defined, module, c.name, c.arity, true),
              uniq: true,
              do: module

        kernel_filter =
          Enum.find_value(c.imports, :all, fn
            {"Kernel", filter} -> filter
            _ -> nil
          end)

        case candidates do
          [module] ->
            {module, as.("import")}

          [] ->
            if MapSet.member?(@kernel, {c.name, c.arity}) and
                 allows?(kernel_filter, c.name, c.arity),
               do: {"Kernel", as.("import")},
               else: {nil, kind}

          _ ->
            {nil, kind}
        end
    end
  end

  defp classify(c, _), do: {c.module, c.kind}

  defp defined?(defined, module, name, arity, public_only) do
    defined
    |> Map.get({module, name}, [])
    |> Enum.any?(fn {min, max, public} -> arity in min..max and (public or not public_only) end)
  end

  defp allows?(:all, _, _), do: true
  defp allows?({:only, set}, name, arity), do: MapSet.member?(set, {name, arity})
  defp allows?({:except, set}, name, arity), do: not MapSet.member?(set, {name, arity})
  defp allows?(_, _, _), do: false

  # --- table references ------------------------------------------------------

  defp table_refs(root, ref_roots, schema_path, schemas, clauses) do
    schema = schema_path |> File.read!() |> JSON.decode!()

    if schema["facts_version"] != 1,
      do: raise(ArgumentError, "--schema-facts #{schema_path} is not a facts v1 document")

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
