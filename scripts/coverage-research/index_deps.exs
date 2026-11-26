# Usage: elixir index_deps.exs <mix.lock file> <hex packages dir> > index.json
# Reads each locked Hex package's source from the cached tarball (in memory)
# and prints {module => %{exports: [[name, min, max]], using: [[name, min, max]]}}.
[lock_file, hexdir] = System.argv()
# The lock is parsed as data, never evaluated.
{:ok, {:%{}, _, lock}} = Code.string_to_quoted(File.read!(lock_file))

defmodule Idx do
  def defs_in(ast) do
    {_, acc} =
      Macro.prewalk(ast, [], fn
        {kind, _, [head | _]} = n, acc when kind in [:def, :defmacro, :defdelegate, :defguard] ->
          case head(head) do
            nil -> {n, acc}
            h -> {n, [h | acc]}
          end
        n, acc -> {n, acc}
      end)
    acc
  end

  def head({:when, _, [h | _]}), do: head(h)
  def head({name, _, args}) when is_atom(name) and name != :unquote and is_list(args) do
    d = Enum.count(args, &match?({:\\, _, _}, &1))
    [Atom.to_string(name), length(args) - d, length(args)]
  end
  def head({name, _, ctx}) when is_atom(name) and is_atom(ctx) and name != :unquote, do: [Atom.to_string(name), 0, 0]
  def head(_), do: nil

  # Top-level modules in a file, with their own (non-nested) public defs and
  # the defs inside a `defmacro __using__` quote.
  def modules(ast, outer \\ nil) do
    {_, acc} =
      Macro.prewalk(ast, [], fn
        {:defmodule, _, [{:__aliases__, _, parts}, [do: body]]}, acc ->
          name = Enum.join(Enum.reject([outer | Enum.map(parts, &to_string/1)], &is_nil/1), ".")
          inner = modules(body, name)
          {nil, [{name, own(body), using(body)} | inner ++ acc]}
        n, acc -> {n, acc}
      end)
    acc
  end

  def own(body) do
    forms = case body do {:__block__, _, f} -> f; f -> [f] end
    for f <- forms, h <- top_def(f), do: h
  end
  defp top_def({kind, _, [head | _]}) when kind in [:def, :defmacro, :defdelegate, :defguard] do
    case head(head), do: (nil -> []; h -> [h])
  end
  defp top_def(_), do: []

  def using(body) do
    {_, acc} =
      Macro.prewalk(body, [], fn
        {:defmacro, _, [{:__using__, _, _} | rest]} = n, acc -> {n, defs_in(rest) ++ acc}
        n, acc -> {n, acc}
      end)
    acc
  end
end

index =
  for {_name, {:{}, _, [:hex, pkg, vsn | _]}} <- lock,
      tar = Path.join(hexdir, "#{pkg}-#{vsn}.tar"),
      File.exists?(tar),
      {:ok, outer} = :erl_tar.extract(String.to_charlist(tar), [:memory]),
      {_, contents} = List.keyfind(outer, ~c"contents.tar.gz", 0),
      {:ok, files} = :erl_tar.extract({:binary, contents}, [:memory, :compressed]),
      {path, src} <- files,
      String.ends_with?(to_string(path), ".ex"),
      {:ok, ast} = (case Code.string_to_quoted(src, emit_warnings: false) do {:ok, a} -> {:ok, a}; _ -> {:ok, nil} end),
      {mod, own, using} <- Idx.modules(ast),
      reduce: %{} do
    acc -> Map.put(acc, mod, %{exports: Enum.uniq(own), using: Enum.uniq(using), package: to_string(pkg)})
  end

IO.puts(JSON.encode!(index))
