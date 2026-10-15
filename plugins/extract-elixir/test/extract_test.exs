# Run: mise exec -- elixir plugins/extract-elixir/test/extract_test.exs
Code.require_file("../source.exs", __DIR__)
Code.require_file("../facts.exs", __DIR__)
ExUnit.start()

defmodule Cartographer.ElixirExtractTest do
  use ExUnit.Case, async: true

  alias Cartographer.ElixirFacts

  @plugin Path.expand("..", __DIR__)
  @fixture Path.expand("fixture", __DIR__)
  @schema Path.expand("schema.json", __DIR__)

  defp fixture_doc(opts \\ []) do
    ElixirFacts.build(
      Keyword.merge([repo: "shop", root: @fixture, source_roots: ["lib", "test"]], opts)
    )
  end

  defp fun(doc, id), do: Enum.find(doc.functions, &(&1.id == "shop:" <> id))

  defp calls_from(doc, from, name \\ nil) do
    doc.calls
    |> Enum.filter(&(&1.from == "shop:" <> from and (name == nil or &1.target.name == name)))
    |> Enum.sort_by(& &1.line)
  end

  defp tmp_root(files) do
    root = Path.join(System.tmp_dir!(), "extract-elixir-#{System.unique_integer([:positive])}")
    on_exit(fn -> File.rm_rf!(root) end)

    for {path, text} <- files do
      File.mkdir_p!(Path.dirname(Path.join(root, path)))
      File.write!(Path.join(root, path), text)
    end

    root
  end

  defp extract(source) do
    ElixirFacts.build(repo: "fixture", root: tmp_root(%{"lib/scope.ex" => source}))
  end

  defp from(doc, function, name) do
    doc.calls
    |> Enum.filter(&(&1.from == "fixture:" <> function and &1.target.name == name))
    |> Enum.sort_by(& &1.line)
  end

  describe "fixture project" do
    test "inventories files, modules and namespaces" do
      doc = fixture_doc()
      assert doc.facts_version == 1

      assert doc.producer == %{
               name: "extract-elixir",
               version: "0.1.0",
               call_precision: "precise"
             }

      assert Enum.map(doc.modules, & &1.id) == [
               "shop:Shop.Billing",
               "shop:Shop.Format",
               "shop:Shop.Orders",
               "shop:Shop.Orders.Order",
               "shop:Shop.OrdersTest"
             ]

      assert Enum.find(doc.modules, &(&1.name == "Shop.Orders.Order")).namespace ==
               ["Shop", "Orders", "Order"]

      categories = Map.new(doc.files, &{&1.path, &1.category})
      assert categories["lib/shop/orders.ex"] == "source"
      assert categories["test/shop/orders_test.exs"] == "test"
      refute Map.has_key?(doc, :table_refs)
      refute Map.has_key?(doc, :revision)
    end

    test "functions carry default arities, merged clauses and kinds" do
      doc = fixture_doc()
      create = fun(doc, "Shop.Orders.create/2")
      assert create.min_arity == 1 and create.arity == 2
      assert create.module == "shop:Shop.Orders"
      assert fun(doc, "Shop.Orders.total/1").clauses == [13, 14]
      assert fun(doc, "Shop.Orders.validate/1").kind == "defp"
      assert fun(doc, "Shop.Orders.refund/1").kind == "defdelegate"
      assert fun(doc, "Shop.OrdersTest.sample/0").file == "test/shop/orders_test.exs"
      assert Enum.all?(doc.functions, &(&1.fingerprint =~ ~r/\A[0-9a-f]{64}\z/))
      # Generated definitions are notes, not functions.
      refute Enum.any?(doc.functions, &(&1.name in ["kg", "lb"]))

      assert [%{kind: "dynamic_definition", file: "lib/shop/format.ex"}] =
               Enum.filter(doc.notes, &(&1.kind == "dynamic_definition"))
    end

    test "calls name alias-expanded targets and are not resolved to ids" do
      doc = fixture_doc()
      refute Enum.any?(doc.calls, &Map.has_key?(&1, :to))

      # Pipes add the piped argument to the arity.
      assert [
               %{
                 target: %{module: "Shop.Orders", name: "validate", arity: 1},
                 kind: "local",
                 line: 9
               }
             ] =
               calls_from(doc, "Shop.Orders.create/2", "validate")

      assert [%{target: %{module: "Shop.Billing", arity: 2}, kind: "remote", line: 10}] =
               calls_from(doc, "Shop.Orders.create/2", "charge")

      assert [%{target: %{module: "Shop.Orders", name: "total"}, kind: "local"}] =
               calls_from(doc, "Shop.Orders.total/1", "total")

      assert [%{target: %{module: "Shop.Billing", name: "refund", arity: 1}, kind: "capture"}] =
               calls_from(doc, "Shop.Orders.refund_all/1", "refund")

      assert [%{target: %{module: "Shop.Billing", name: "refund"}, kind: "delegate"}] =
               calls_from(doc, "Shop.Orders.refund/1")

      assert [%{target: %{module: "Shop.Billing", name: "refund", arity: 1}, kind: "apply"}] =
               calls_from(doc, "Shop.Orders.run_known/0")

      assert [%{target: %{name: "apply", arity: 3} = t, kind: "dynamic"}] =
               calls_from(doc, "Shop.Orders.run_later/2")

      refute Map.has_key?(t, :module)

      assert [%{target: %{module: "Shop.Format", name: "cents"}, kind: "import"}] =
               calls_from(doc, "Shop.Orders.label/1", "cents")

      assert [%{target: %{module: "Kernel"}, kind: "import"}] =
               calls_from(doc, "Shop.Orders.label/1", "to_string")

      # Imports from dependencies cannot be attributed: no module.
      assert [%{target: t, kind: "local_or_import"}] =
               calls_from(doc, "Shop.Orders.Order.recent_query/0", "from")

      refute Map.has_key?(t, :module)

      # String interpolation is not a Kernel.to_string call; field access is not a call.
      assert calls_from(doc, "Shop.Format.cents/1") == []

      refute Enum.any?(
               doc.calls,
               &(&1.target.name in ["price", "total"] and &1.kind == "dynamic")
             )

      refute Enum.any?(doc.calls, &(&1.target.name in ["<>", "+", "|>", "=="]))
    end

    test "table_refs come from text and Ecto schema declarations when schema facts are given" do
      doc = fixture_doc(schema_facts: @schema)

      assert %{table: "public.orders", text: "schema \"orders\" do"} =
               ref = Enum.find(doc.table_refs, &(&1.file == "lib/shop/order.ex" and &1.line == 5))

      refute Map.has_key?(ref, :function)

      assert %{function: "shop:Shop.Orders.Order.recent_query/0"} =
               Enum.find(doc.table_refs, &(&1.file == "lib/shop/order.ex" and &1.line == 12))

      migration = "priv/repo/migrations/20240101000000_create_orders.exs"

      assert ["public.customers", "public.orders"] ==
               doc.table_refs
               |> Enum.filter(&(&1.file == migration))
               |> Enum.map(& &1.table)
               |> Enum.sort()

      assert Enum.find(doc.files, &(&1.path == migration)).category == "migration"
      # The comment line "# orders placed this week" is skipped.
      refute Enum.any?(doc.table_refs, &(&1.file == "lib/shop/order.ex" and &1.line == 11))
    end

    test "CLI output is deterministic JSON" do
      args = [
        Path.join(@plugin, "extract.exs"),
        "--repo",
        "shop",
        "--root",
        @fixture,
        "--source-roots",
        "lib,test",
        "--revision",
        "abc123",
        "--schema-facts",
        @schema
      ]

      {first, 0} = System.cmd("elixir", args)
      {second, 0} = System.cmd("elixir", args)
      assert first == second
      decoded = JSON.decode!(first)
      assert decoded["revision"] == "abc123"
      assert String.starts_with?(first, ~s({"facts_version":1,"producer":))
      assert length(decoded["functions"]) == length(fixture_doc().functions)
    end

    test "CLI rejects bad arguments without stdout" do
      {out, status} =
        System.cmd(
          "elixir",
          [Path.join(@plugin, "extract.exs"), "--repo", "a:b", "--root", @fixture],
          stderr_to_stdout: false
        )

      assert status == 2 and out == ""

      {_, status} =
        System.cmd(
          "elixir",
          [
            Path.join(@plugin, "extract.exs"),
            "--repo",
            "x",
            "--root",
            @fixture,
            "--source-roots",
            "../etc"
          ],
          stderr_to_stdout: true
        )

      assert status == 2
    end
  end

  describe "fingerprints" do
    defp fingerprints(edit) do
      root = tmp_root(%{})
      File.cp_r!(@fixture, root)
      path = Path.join(root, "lib/shop/orders.ex")
      old = File.read!(path)
      new = edit.(old)
      assert new != old
      File.write!(path, new)
      doc = ElixirFacts.build(repo: "shop", root: root)
      Map.new(doc.functions, &{&1.id, &1.fingerprint})
    end

    test "survive whitespace and comment edits, change with the body" do
      base = Map.new(fixture_doc().functions, &{&1.id, &1.fingerprint})

      reformatted =
        fingerprints(fn text ->
          text
          |> String.replace(
            "def total([]), do: 0",
            "# empty list\n  def total( [] ),\n    do: 0\n"
          )
          |> String.replace(
            "  attrs\n    |> validate()",
            "\n  attrs   # pipeline\n\n    |> validate()"
          )
        end)

      assert reformatted["shop:Shop.Orders.total/1"] == base["shop:Shop.Orders.total/1"]
      assert reformatted["shop:Shop.Orders.create/2"] == base["shop:Shop.Orders.create/2"]

      changed =
        fingerprints(&String.replace(&1, "item.price + total(rest)", "item.price * total(rest)"))

      refute changed["shop:Shop.Orders.total/1"] == base["shop:Shop.Orders.total/1"]
      assert changed["shop:Shop.Orders.create/2"] == base["shop:Shop.Orders.create/2"]
    end
  end

  describe "robustness" do
    test "parse errors become notes and do not abort" do
      root =
        tmp_root(%{
          "lib/bad.ex" => "defmodule Bad do\n  def broken(\nend\n",
          "lib/good.ex" => "defmodule Good do\n  def ok, do: :ok\nend\n"
        })

      doc = ElixirFacts.build(repo: "fixture", root: root)
      assert [%{kind: "parse_error", file: "lib/bad.ex", message: "line " <> _}] = doc.notes
      assert Enum.map(doc.functions, & &1.id) == ["fixture:Good.ok/0"]
      assert Enum.any?(doc.files, &(&1.path == "lib/bad.ex"))
    end

    test "symlinked sources are skipped with a note" do
      outside = tmp_root(%{"secret.ex" => "defmodule Secret do\n  def x, do: 1\nend\n"})
      root = tmp_root(%{"lib/real.ex" => "defmodule Real do\nend\n"})
      File.ln_s!(Path.join(outside, "secret.ex"), Path.join(root, "lib/link.ex"))
      doc = ElixirFacts.build(repo: "fixture", root: root)
      assert Enum.map(doc.modules, & &1.name) == ["Real"]
      assert [%{kind: "symlink_skipped", file: "lib/link.ex"}] = doc.notes
    end

    test "the plugin hardcodes no repository names or hosts" do
      for file <- ~w(source.exs facts.exs extract.exs),
          text = File.read!(Path.join(@plugin, file)),
          bad <- ~w(breakdown Applesauce github.com),
          do: refute(String.contains?(text, bad), "#{file} mentions #{bad}")
    end
  end

  # Ported from breakdown-admin tools/system_map/test/source_test.exs.
  describe "lexical scope" do
    test "lexical aliases, pipe arities, defaults and guards preserve source evidence" do
      doc =
        extract(~S'''
        defmodule Sample do
          alias Sample.Helper, as: H
          def run(x \\ 1)
          def run(x) when H.allowed?(x), do: x |> H.work()
          def capture, do: &H.work/1
          def operators(x, y), do: {x + y, x == y, x != y, x > y, x and y, [x | y], <<x::8>>, for(z <- 1..2, do: run(z))}
          def quoted, do: quote(do: H.not_executed())
        end
        defmodule Sample.Helper do
          def work(x), do: x
          def allowed?(x), do: is_integer(x)
        end
        ''')

      assert doc.notes == []

      assert Enum.any?(
               doc.calls,
               &(&1.target[:module] == "Sample.Helper" and &1.target.name == "work" and
                   &1.target.arity == 1)
             )

      assert Enum.any?(
               doc.calls,
               &(&1.target[:module] == "Sample.Helper" and &1.target.name == "allowed?")
             )

      refute Enum.any?(doc.calls, &(&1.target.name == "not_executed"))

      refute Enum.any?(
               doc.calls,
               &(&1.target.name in ["+", "==", "!=", ">", "and", "|", "::", "<-", "..", "\\\\"])
             )

      assert [%{target: %{module: "Sample", arity: 1}, kind: "local"}] =
               from(doc, "Sample.operators/2", "run")

      assert %{min_arity: 0, clauses: [3, 4]} = Enum.find(doc.functions, &(&1.name == "run"))
    end

    test "nested multi-part module names are scoped while explicit Elixir names are absolute" do
      doc =
        extract(~S'''
        defmodule Outer do
          alias Unrelated, as: A
          defmodule A.B do
            def run, do: :ok
          end
          defmodule Elixir.Absolute do
            def run, do: :ok
          end
          def call, do: A.B.run()
        end
        ''')

      assert doc.functions |> Enum.map(& &1.module) |> Enum.sort() ==
               ["fixture:Absolute", "fixture:Outer", "fixture:Outer.A.B"]

      # The nested defmodule re-aliases A to Outer.A for the rest of Outer.
      assert [%{target: %{module: "Outer.A.B"}}] = from(doc, "Outer.call/0", "run")
    end

    test "as rebinding uses the declared alias name rather than its old target" do
      doc =
        extract(~S'''
        defmodule ReviewCaller do
          alias ReviewA, as: H
          def run do
            alias ReviewB, as: H
            H.go()
          end
          def unchanged, do: H.go()
        end
        ''')

      assert [%{target: %{module: "ReviewB"}}] = from(doc, "ReviewCaller.run/0", "go")
      assert [%{target: %{module: "ReviewA"}}] = from(doc, "ReviewCaller.unchanged/0", "go")
    end

    test "conditional branches and following expressions retain their own lexical aliases" do
      doc =
        extract(~S'''
        defmodule ScopeCaller do
          alias ScopeA, as: ScopeB
          def run(x) do
            if x do
              alias Elixir.ScopeB
              ScopeB.go()
            else
              ScopeB.go()
            end
            ScopeB.go()
          end
        end
        ''')

      assert Enum.map(from(doc, "ScopeCaller.run/1", "go"), & &1.target.module) ==
               ["ScopeB", "ScopeA", "ScopeA"]
    end

    test "case and anonymous-function clauses cannot leak aliases into sibling clauses" do
      doc =
        extract(~S'''
        defmodule ClauseCaller do
          alias ClauseA, as: H
          def run(x) do
            case x do
              true ->
                alias ClauseB, as: H
                H.go()
              false -> H.go()
            end
            fn
              true ->
                alias ClauseB, as: H
                H.go()
              false -> H.go()
            end
            H.go()
          end
        end
        ''')

      assert Enum.map(from(doc, "ClauseCaller.run/1", "go"), & &1.target.module) ==
               ["ClauseB", "ClauseA", "ClauseB", "ClauseA", "ClauseA"]
    end

    test "import only/except filters decide attribution; locals win" do
      doc =
        extract(~S'''
        defmodule Lib.A do
          def shared(x), do: x
          def only_a(x), do: x
        end
        defmodule Lib.B do
          def shared(x), do: x
        end
        defmodule Caller do
          import Lib.A, only: [only_a: 1]
          import Lib.B
          def one(x), do: {only_a(x), shared(x), mine(x)}
          def two(x), do: mod_fun(x) |> other.call()
          defp mine(x), do: x
        end
        ''')

      assert [%{target: %{module: "Lib.A"}, kind: "import"}] = from(doc, "Caller.one/1", "only_a")
      assert [%{target: %{module: "Lib.B"}, kind: "import"}] = from(doc, "Caller.one/1", "shared")
      assert [%{target: %{module: "Caller"}, kind: "local"}] = from(doc, "Caller.one/1", "mine")
      assert [%{kind: "local_or_import"}] = from(doc, "Caller.two/1", "mod_fun")
      assert [%{kind: "dynamic", target: %{arity: 1}}] = from(doc, "Caller.two/1", "call")
    end
  end
end
