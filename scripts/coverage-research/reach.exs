# Superset library reader probe. Usage: elixir reach.exs <lock> <hexdir> <Module[:opt=Mod,...]>...
[lock_file, hexdir | starts] = System.argv()
{:ok, lock_ast} = Code.string_to_quoted(File.read!(lock_file))
{:%{}, _, entries} = lock_ast
pkgs = for {_, {:{}, _, [:hex, pkg, vsn | _]}} <- entries, do: {pkg, vsn}

defmodule R do
  # Per module: defs inside any quote (skipping nested defmodule/defimpl),
  # with `for x <- [literal atoms]` + `unquote(x)` names expanded, and the
  # literal modules its quotes use/import/@before_compile, or any alias it
  # names in a quote.
  def scan(mod_body, modname) do
    {_, acc} =
      Macro.prewalk(mod_body, %{defs: [], refs: [], imports: []}, fn
        {:quote, _, args} = n, acc -> {n, quote_scan(args, modname, acc, %{})}
        n, acc -> {n, acc}
      end)
    forms = case mod_body do {:__block__, _, f} -> f; f -> [f] end
    exports = for {k, _, [h | _]} <- forms, k in [:def, :defmacro, :defdelegate, :defguard], x = head(h, %{}), x != nil, do: x
    top_uses = for {:use, _, [{:__aliases__, _, ps} | _]} <- forms, do: Enum.map_join(ps, ".", &to_string/1)
    acc |> Map.put(:exports, exports) |> Map.update!(:refs, &(top_uses ++ &1))
  end

  def quote_scan(ast, modname, acc, binds) do
    {_, acc} =
      Macro.prewalk(ast, acc, fn
        {kind, _, _}, acc when kind in [:defmodule, :defimpl, :defprotocol] -> {nil, acc}
        {:for, _, args}, acc when is_list(args) ->
          gens = for {:<-, _, [{v, _, c}, list]} <- args, is_atom(c), is_list(list), do: {v, Enum.filter(list, &is_atom/1)}
          body = case List.last(args) do kw when is_list(kw) -> Keyword.get(kw, :do); _ -> nil end
          acc = Enum.reduce(gens, acc, fn {v, atoms}, a -> Enum.reduce(atoms, a, fn at, a2 -> quote_scan(body, modname, a2, Map.put(binds, v, at)) end) end)
          {nil, acc}
        {kind, _, [head | _]} = n, acc when kind in [:def, :defmacro, :defdelegate, :defguard] ->
          case head(head, binds) do nil -> {n, acc}; h -> {n, %{acc | defs: [h | acc.defs]}} end
        {:import, _, [m | _]} = n, acc -> {n, add_import(add_ref(acc, m, modname), m, modname)}
        {k, _, [m | _]} = n, acc when k in [:use, :require] -> {n, add_ref(acc, m, modname)}
        {:@, _, [{:before_compile, _, [m]}]} = n, acc -> {n, add_ref(acc, m, modname)}
        n, acc -> {n, acc}
      end)
    acc
  end

  defp add_ref(acc, {:__aliases__, _, parts}, _), do: %{acc | refs: [Enum.map_join(parts, ".", &to_string/1) | acc.refs]}
  defp add_ref(acc, {:unquote, _, [{:__MODULE__, _, _}]}, m), do: %{acc | refs: [m | acc.refs]}
  defp add_ref(acc, {:__MODULE__, _, _}, m), do: %{acc | refs: [m | acc.refs]}
  defp add_ref(acc, _, _), do: acc
  defp add_import(acc, m, modname) do
    r = add_ref(%{acc | refs: []}, m, modname).refs
    %{acc | imports: r ++ acc.imports}
  end

  def head({:when, _, [h | _]}, b), do: head(h, b)
  def head({:unquote, _, [{v, _, c}]}, b) when is_atom(c), do: (case b[v] do nil -> nil; a -> [to_string(a), -1, -1] end)
  def head({{:unquote, _, [{v, _, c}]}, _, args}, b) when is_atom(c) and is_list(args), do: (case b[v] do nil -> nil; a -> [to_string(a), length(args) - Enum.count(args, &match?({:\\, _, _}, &1)), length(args)] end)
  def head({name, _, args}, _) when is_atom(name) and name != :unquote and is_list(args), do: [to_string(name), length(args) - Enum.count(args, &match?({:\\, _, _}, &1)), length(args)]
  def head({name, _, c}, _) when is_atom(name) and is_atom(c) and name != :unquote, do: [to_string(name), 0, 0]
  def head(_, _), do: nil

  def modules(ast, outer \\ nil) do
    {_, acc} =
      Macro.prewalk(ast, [], fn
        {:defmodule, _, [{:__aliases__, _, parts}, [do: body]]}, acc ->
          name = Enum.join(Enum.reject([outer | Enum.map(parts, &to_string/1)], &is_nil/1), ".")
          {nil, [{name, body} | modules(body, name) ++ acc]}
        n, acc -> {n, acc}
      end)
    acc
  end
end

idx =
  for {pkg, vsn} <- pkgs, tar = Path.join(hexdir, "#{pkg}-#{vsn}.tar"), File.exists?(tar),
      {:ok, outer} = :erl_tar.extract(String.to_charlist(tar), [:memory]),
      {_, c} = List.keyfind(outer, ~c"contents.tar.gz", 0),
      {:ok, files} = :erl_tar.extract({:binary, c}, [:memory, :compressed]),
      {p, src} <- files, String.ends_with?(to_string(p), ".ex"),
      {:ok, ast} <- [Code.string_to_quoted(src, emit_warnings: false)],
      {m, body} <- R.modules(ast), into: %{}, do: {m, R.scan(body, m)}

for start <- starts do
  [mod | opts] = String.split(start, ":")
  extra = for o <- opts, [_, v] = String.split(o, "="), do: v
  {seen, _} =
    Stream.iterate({MapSet.new(), [mod | extra]}, fn
      {seen, []} -> {seen, []}
      {seen, [m | rest]} ->
        if MapSet.member?(seen, m) or not Map.has_key?(idx, m), do: {MapSet.put(seen, m), rest},
        else: {MapSet.put(seen, m), idx[m].refs ++ rest}
    end) |> Enum.find(fn {_, q} -> q == [] end)
  imported = seen |> Enum.flat_map(&(get_in(idx, [&1, :imports]) || [])) |> Enum.uniq()
  defs = (Enum.flat_map(seen, &(get_in(idx, [&1, :defs]) || [])) ++ Enum.flat_map(imported, &(get_in(idx, [&1, :exports]) || []))) |> Enum.uniq()
  names = MapSet.new(defs, &hd/1)
  IO.puts("#{start}: #{MapSet.size(seen)} modules reached, #{length(defs)} defs; " <>
    Enum.map_join(~w(all one insert! transaction query! assign to_form sigil_p get_by post gettext assert), " ", &"#{&1}=#{MapSet.member?(names, &1)}"))
end
