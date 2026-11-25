# Collects the review probes' shapes into one corpus term:
# [{"probe/name", %{path => source}}]. A shape is a `build.("name", %{files})`
# call (its map evaluated with the script's top-level literal bindings) or
# a `File.write!(Path.join(root, path), source)` of a literal source.
files = System.argv()

literal? = fn ast ->
  {_, ok} = Macro.prewalk(ast, true, fn
    {name, _, ctx} = n, acc when is_atom(name) and is_atom(ctx) -> {n, false and acc}
    n, acc -> {n, acc}
  end)
  ok
end

shapes =
  Enum.flat_map(files, fn f ->
    {:ok, ast} = Code.string_to_quoted(File.read!(f))
    probe = Path.basename(f, ".exs")
    top = case ast do {:__block__, _, forms} -> forms; form -> [form] end

    binding =
      Enum.reduce(top, [], fn
        {:=, _, [{v, _, nil}, rhs]}, b when is_atom(v) ->
          if literal?.(rhs) do
            try do
              {val, _} = Code.eval_quoted(rhs)
              if is_binary(val), do: Keyword.put(b, v, val), else: b
            rescue
              _ -> b
            end
          else
            b
          end
        _, b -> b
      end)

    {_, acc} =
      Macro.prewalk(ast, {[], 0}, fn
        {{:., _, [{fun, _, nil}]}, _, [name, {:%{}, _, _} = map | _]} = n, {acc, i} when fun in [:build, :b, :run] and is_binary(name) ->
          try do
            {fs, _} = Code.eval_quoted(map, binding)
            {n, {[{probe <> "/" <> name, fs} | acc], i}}
          rescue
            _ -> IO.puts(:stderr, "skipped #{probe}/#{name}"); {n, {acc, i}}
          end

        {{:., _, [{:__aliases__, _, [:File]}, :write!]}, _, [{{:., _, [{:__aliases__, _, [:Path]}, :join]}, _, [_root, path]}, src]} = n, {acc, i}
        when is_binary(path) ->
          if literal?.(src) do
            {text, _} = Code.eval_quoted(src)
            {n, {[{"#{probe}/write-#{i + 1}", %{path => text}} | acc], i + 1}}
          else
            {n, {acc, i}}
          end

        n, acc -> {n, acc}
      end)

    acc |> elem(0) |> Enum.reverse()
  end)
  |> Enum.uniq_by(&elem(&1, 0))

IO.puts(:stderr, "#{length(shapes)} shapes")
IO.puts(inspect(shapes, limit: :infinity, printable_limit: :infinity, pretty: true))
