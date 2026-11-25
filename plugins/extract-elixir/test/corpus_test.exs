# Run: elixir plugins/extract-elixir/test/corpus_test.exs
#
# The regression corpus (map-coverage-2 decision 20): every probe shape
# from cartographer#35's review rounds, plus shapes for the evidence rules,
# built with the stub library package. Two oracles, independent of the
# extractor's own rules:
#   - never a wrong library: a call labelled external never names a
#     function a module in the caller's textual scope defines (its own
#     module, what it uses or imports, and what their quotes use or
#     import), and never sits where a computed `def` is in scope;
#   - never a wrong host: an HTTP ref's host is written in a base_url: or
#     module-attribute line of its own file.
ExUnit.start()
for f <- ~w(heex_tokenizer.exs heex.exs source.exs deps.exs http.exs facts.exs), do: Code.require_file("../" <> f, __DIR__)
Code.require_file("support/hex_fixture.exs", __DIR__)
Code.require_file("support/stub_libs.exs", __DIR__)

defmodule Cartographer.CorpusTest do
  use ExUnit.Case, async: true

  {shapes, _} = Code.eval_file(Path.join(__DIR__, "corpus/shapes.exs"))
  @shapes shapes

  @extra [
    {"evidence/collision-out-of-scope",
     %{
       "lib/mine.ex" => "defmodule Mine do\n  def change(a, b), do: {a, b}\nend\n",
       "lib/u.ex" => "defmodule U do\n  import Ecto.Changeset\n  def c(x), do: change(x, %{})\nend\n"
     }},
    {"evidence/dsl-and-pin",
     %{
       "lib/q.ex" =>
         "defmodule Q do\n  import Ecto.Query\n  def count(x), do: x\n  def q(n), do: from(u in \"t\", where: count(u.id) > ^count(n))\nend\n"
     }},
    {"evidence/conditional-library-def",
     %{"lib/r.ex" => "defmodule App.Repo do\n  use Ecto.Repo, otp_app: :app\nend\n", "lib/w.ex" => "defmodule W do\n  def go(x), do: App.Repo.get(x, 1)\nend\n"}},
    {"evidence/missing-package",
     %{"lib/m.ex" => "defmodule M do\n  import Missing.Lib\n  def go(x), do: lib_fun(x)\nend\n"}},
    {"evidence/unreadable-import",
     %{"lib/m.ex" => "defmodule M do\n  import Ecto.Changeset, only: opts()\n  def opts, do: []\n  def go(x), do: change(x)\nend\n"}},
    {"evidence/two-locks",
     %{
       "a/lib/a.ex" => "defmodule A do\n  import Ecto.Changeset\n  def go(x), do: change(x)\nend\n",
       "b/lib/b.ex" => "defmodule B do\n  import Ecto.Changeset\n  def go(x), do: change(x)\nend\n",
       "b/mix.lock" => "%{}\n"
     }}
  ]

  defp root_for(name, files) do
    root = Path.join(System.tmp_dir!(), "corpus-#{System.unique_integer([:positive])}")
    on_exit(fn -> File.rm_rf!(root) end)

    for {p, text} <- files do
      File.mkdir_p!(Path.dirname(Path.join(root, p)))
      File.write!(Path.join(root, p), text)
    end

    cond do
      name == "evidence/two-locks" ->
      # a/ reads the stubs; b/ has its own, empty lock.
        Cartographer.StubLibs.install(Path.join(root, "a"))
        File.rename!(Path.join(root, "a/.hex"), Path.join(root, ".hex"))

      String.starts_with?(name, "evidence/") ->
        Cartographer.StubLibs.install(root)

      true ->
        # The adversary: every library module the shape names exports, and
        # generates through its __using__, every name the shape calls. A
        # project function can then only stay unlabelled through the rules
        # (project first, holes), never through a library's silence.
        lock = Cartographer.HexFixture.write(Path.join(root, ".hex"), [{"adversary", "1.0.0", %{"lib/adv.ex" => adversary(files)}}])
        File.write!(Path.join(root, "mix.lock"), lock)
    end

    root
  end

  defp build(name, files) do
    root = root_for(name, files)
    roots = files |> Map.keys() |> Enum.map(&(&1 |> Path.split() |> hd())) |> Enum.uniq() |> Enum.map(&if(&1 == "lib", do: "lib", else: &1 <> "/lib"))
    roots = Enum.filter(roots, &File.dir?(Path.join(root, &1)))
    Cartographer.ElixirFacts.build(repo: "fx", root: root, source_roots: roots, hex_cache: Path.join(root, ".hex"))
  end

  defp adversary(files) do
    mods = modules_of(files)
    project = MapSet.new(Map.keys(mods))
    libs = mods |> Enum.flat_map(fn {_, m} -> m.refs end) |> Enum.uniq() |> Enum.reject(&MapSet.member?(project, &1))

    called =
      for {_path, src} <- files, {:ok, ast} <- [Code.string_to_quoted(src, emit_warnings: false)], reduce: MapSet.new() do
        acc ->
          {_, acc} =
            Macro.prewalk(ast, acc, fn
              {{:., _, [_, f]}, _, args} = n, acc when is_atom(f) and is_list(args) -> {n, MapSet.put(acc, {f, length(args)})}
              {f, _, args} = n, acc when is_atom(f) and is_list(args) ->
                if Macro.operator?(f, length(args)) or f in [:def, :defp, :defmodule, :defmacro, :quote, :unquote, :__aliases__, :__block__, :use, :import, :alias, :require, :fn, :->, :case, :cond, :if, :for, :with, :=, :{}, :%{}, :<<>>, :@, :&, :^, :when, :do, :__MODULE__],
                  do: {n, acc},
                  else: {n, MapSet.put(acc, {f, length(args)})}
              n, acc -> {n, acc}
            end)

          acc
      end

    defs =
      for {f, a} <- called, Regex.match?(~r/^[a-z_][a-zA-Z0-9_]*[?!]?$/, to_string(f)) do
        args = Enum.map_join(1..a//1, ", ", &"_a#{&1}")
        "  def #{f}(#{args}), do: nil"
      end
      |> Enum.join("\n")

    Enum.map_join(libs, "\n", fn lib ->
      "defmodule #{lib} do\n  defmacro __using__(_) do\n    quote do\n      import #{lib}\n" <>
        String.replace(defs, "  def ", "      def ") <> "\n    end\n  end\n" <> defs <> "\nend\n"
    end)
  end

  # --- the oracles ---------------------------------------------------------

  # Per module: its source text, its definitions (anywhere, quotes
  # included) with arity ranges, whether it has a computed def, and the
  # modules it names in use/import.
  defp modules_of(files) do
    for {path, src} <- files, String.ends_with?(path, [".ex", ".exs"]),
        {:ok, ast} <- [Code.string_to_quoted(src, emit_warnings: false)],
        {name, body} <- defmodules(ast, nil),
        into: %{} do
      {_, acc} =
        Macro.prewalk(body, %{defs: [], computed: false, refs: []}, fn
          # A nested module's definitions are its own, not this module's.
          {:defmodule, _, _}, acc ->
            {nil, acc}

          {k, _, [head | _]} = n, acc when k in [:def, :defp, :defmacro, :defmacrop, :defdelegate] ->
            case head_of(head) do
              :computed -> {n, %{acc | computed: true}}
              nil -> {n, acc}
              h -> {n, %{acc | defs: [h | acc.defs]}}
            end

          {k, _, [{:__aliases__, _, parts} | _]} = n, acc when k in [:use, :import] ->
            {n, %{acc | refs: [Enum.join(parts, ".") | acc.refs]}}

          n, acc ->
            {n, acc}
        end)

      {name, Map.put(acc, :path, path)}
    end
  end

  defp defmodules(ast, outer) do
    {_, acc} =
      Macro.prewalk(ast, [], fn
        {:defmodule, _, [{:__aliases__, _, parts}, [do: body]]}, acc ->
          name = Enum.join(Enum.reject([outer | Enum.map(parts, &to_string/1)], &is_nil/1), ".")
          {nil, [{name, body} | defmodules(body, name) ++ acc]}

        n, acc ->
          {n, acc}
      end)

    acc
  end

  defp head_of({:when, _, [h | _]}), do: head_of(h)
  defp head_of({:unquote, _, _}), do: :computed
  defp head_of({{:unquote, _, _}, _, _}), do: :computed

  defp head_of({name, _, args}) when is_atom(name) and is_list(args) do
    d = Enum.count(args, &match?({:\\, _, _}, &1))
    {to_string(name), length(args) - d, length(args)}
  end

  defp head_of({name, _, c}) when is_atom(name) and is_atom(c), do: {to_string(name), 0, 0}
  defp head_of(_), do: nil

  defp scope(mods, module, depth \\ 0) do
    case Map.get(mods, module) do
      nil -> []
      m -> [module | if(depth < 4, do: Enum.flat_map(m.refs, &scope(mods, &1, depth + 1)), else: [])]
    end
  end

  defp wrong_libraries(doc, files) do
    mods = modules_of(files)

    # Query vocabulary is decided before the project's own definitions
    # (map-coverage-2 decision 25), so it's exempt here.
    for c <- doc.calls, c.kind == "external", c.target[:module] not in ["Ecto.Query.API", "Ecto.Query.WindowAPI"] do
      [_, from_mod] = Regex.run(~r/^fx:(.*)\.[^.]+\/\d+$/, c.from)
      # The caller's scope, and the target module when it's the project's
      # (a remote call to a function that module defines itself).
      target = c.target[:module]
      in_scope = (scope(mods, from_mod) ++ if(Map.has_key?(mods, target), do: [target], else: [])) |> Enum.uniq()
      a = c.target.arity

      clash =
        Enum.find(in_scope, fn m ->
          Map.get(mods, m).computed or
            Enum.any?(Map.get(mods, m).defs, fn {n, lo, hi} -> n == c.target.name and a >= lo and a <= hi end)
        end)

      if clash, do: "#{c.from} -> #{inspect(c.target)} is labelled external, but #{clash} (in scope) defines it"
    end
    |> Enum.reject(&is_nil/1)
  end

  defp wrong_hosts(doc, files) do
    for r <- Map.get(doc, :http_refs, []), is_binary(r.base), String.starts_with?(r.base, "http") do
      host = URI.parse(r.base).host
      src = Map.get(files, r.file, "")

      written =
        src
        |> String.split("\n")
        |> Enum.any?(fn line ->
          l = String.downcase(line)
          String.contains?(l, host) and (String.contains?(l, "base") or String.match?(l, ~r/^\s*@\w+/))
        end)

      unless written, do: "#{r.from} has base #{r.base}, which #{r.file} doesn't write as a base"
    end
    |> Enum.reject(&is_nil/1)
  end

  # --- the tests -----------------------------------------------------------

  for {name, files} <- @shapes ++ @extra do
    @name name
    @files files
    test "#{name}: never a wrong library or host" do
      doc = build(@name, @files)
      assert wrong_libraries(doc, @files) == []
      assert wrong_hosts(doc, @files) == []
    end
  end

  # HTTP shapes: each ref's base and path, as review rounds 5-7 of
  # cartographer#35 confirmed them (test/corpus/http_expected.exs).
  {expected, _} = Code.eval_file(Path.join(__DIR__, "corpus/http_expected.exs"))
  @http_expected expected

  test "HTTP shapes keep their reviewed bases" do
    got =
      for {name, files} <- @shapes, String.starts_with?(name, "http"), into: %{} do
        doc = build(name, files)
        {name, doc |> Map.get(:http_refs, []) |> Enum.map(&{&1.from, &1.base, &1.path}) |> Enum.sort()}
      end

    if System.get_env("CORPUS_RECORD"), do: File.write!(Path.join(__DIR__, "corpus/http_expected.exs"), inspect(got, pretty: true, limit: :infinity, printable_limit: :infinity))
    assert got == @http_expected
  end

  # The evidence rules' own shapes, with their expected outcomes.
  test "evidence shapes come out as decided" do
    out = fn name, from, fun ->
      {_, files} = Enum.find(@extra, &(elem(&1, 0) == name))
      doc = build(name, files)
      for c <- doc.calls, c.from == "fx:" <> from, c.target.name == fun, do: {c.kind, c.target[:module]}
    end

    assert out.("evidence/collision-out-of-scope", "U.c/1", "change") == [{"external", "Ecto.Changeset"}]
    assert {"external", "Ecto.Query.API"} in out.("evidence/dsl-and-pin", "Q.q/1", "count")
    assert {"local", "Q"} in out.("evidence/dsl-and-pin", "Q.q/1", "count")
    # get/3 is defined under no condition in the stub; Repo.get is generated.
    assert out.("evidence/conditional-library-def", "W.go/1", "get") == [{"external", "App.Repo"}]
    assert out.("evidence/missing-package", "M.go/1", "lib_fun") == [{"local_or_import", nil}]
    assert out.("evidence/unreadable-import", "M.go/1", "change") == [{"local_or_import", nil}]
    assert out.("evidence/two-locks", "A.go/1", "change") == [{"external", "Ecto.Changeset"}]
    assert out.("evidence/two-locks", "B.go/1", "change") == [{"local_or_import", nil}]
  end
end
