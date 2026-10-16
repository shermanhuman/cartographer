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

  defp atom_exists?(name) do
    _ = String.to_existing_atom(name)
    true
  rescue
    ArgumentError -> false
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

    test "table_refs in Elixir skip docs and prose, keeping atoms, strings and SQL" do
      root =
        tmp_root(%{
          "lib/a.ex" => ~S'''
          defmodule A do
            @moduledoc """
            Reads orders and customers.
            """
            @doc "Lists orders."
            def list(orders), do: orders
            def q, do: from(o in "orders")
            def t, do: :customers
            def sql, do: "SELECT * FROM customers JOIN orders ON true"
          end
          ''',
          "scripts/x.sh" => "psql -c 'select 1' orders\n"
        })

      doc = ElixirFacts.build(repo: "fixture", root: root, schema_facts: @schema)

      assert Enum.map(doc.table_refs, &{&1.file, &1.line, &1.table}) == [
               {"lib/a.ex", 7, "public.orders"},
               {"lib/a.ex", 8, "public.customers"},
               {"lib/a.ex", 9, "public.customers"},
               {"lib/a.ex", 9, "public.orders"},
               {"scripts/x.sh", 1, "public.orders"}
             ]
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

    test "invalid UTF-8 in a source or ref file is repaired, not fatal" do
      root =
        tmp_root(%{
          "lib/latin1.ex" => "defmodule Latin1 do\n  def name, do: \"caf\xe9\"\nend\n",
          "priv/repo/migrations/1_orders.exs" =>
            "defmodule M do\n  def change, do: create table(:orders) # caf\xe9\nend\n",
          "scripts/load.sql" => "insert into orders values ('caf\xe9');\n"
        })

      doc = ElixirFacts.build(repo: "fixture", root: root, schema_facts: @schema)
      json = ElixirFacts.encode(doc)
      assert String.valid?(json)
      decoded = JSON.decode!(json)
      assert Enum.map(decoded["functions"], & &1["id"]) == ["fixture:Latin1.name/0"]

      assert [%{"kind" => "invalid_utf8", "file" => "lib/latin1.ex"}] =
               Enum.filter(decoded["notes"], &(&1["kind"] == "invalid_utf8"))

      texts = Map.new(decoded["table_refs"], &{&1["file"], &1["text"]})
      assert texts["scripts/load.sql"] == "insert into orders values ('caf\uFFFD');"
      assert texts["priv/repo/migrations/1_orders.exs"] =~ "\uFFFD"
    end

    test "unknown names in source do not grow the atom table" do
      tag = System.unique_integer([:positive])
      names = for i <- 1..1000, do: "zz_#{tag}_#{i}"
      # Sigil names are uppercase letters only.
      sigil = fn i -> for <<d <- "#{tag}#{i}">>, into: "ZZ", do: <<d - ?0 + ?A>> end

      source =
        "defmodule ZzMod#{tag} do\n" <>
          Enum.map_join(Enum.with_index(names), "\n", fn {n, i} ->
            "  def #{n}(#{n}_arg, #{n}_kw: 1), do: Zz#{tag}#{i}.#{n}(:#{n}, ~#{sigil.(i)}\"x\", \"#{n}\": 1)"
          end) <> "\nend\n"

      before = :erlang.system_info(:atom_count)
      doc = ElixirFacts.build(repo: "fixture", root: tmp_root(%{"lib/many.ex" => source}))
      assert doc.notes == []
      assert length(doc.functions) == 1000
      assert :erlang.system_info(:atom_count) - before < 200
      refute Enum.any?(names, &atom_exists?/1)
    end

    test "the atom budget backstop skips a file with a note" do
      acc =
        Cartographer.ElixirSource.parse(
          Cartographer.ElixirSource.new(),
          "defmodule A do\nend",
          "lib/a.ex",
          "x",
          atom_budget: 0
        )

      assert [%{kind: "atom_budget", file: "lib/a.ex"}] = acc.notes
      assert acc.modules == []
    end

    test "glob characters in the root are literal and an empty root is noted" do
      parent = tmp_root(%{})
      root = Path.join(parent, "pro[j]{a,b}*?")
      File.mkdir_p!(Path.join(root, "lib"))
      File.write!(Path.join(root, "lib/a.ex"), "defmodule A do\n  def x, do: 1\nend\n")
      doc = ElixirFacts.build(repo: "fixture", root: root, source_roots: ["lib", "missing"])
      assert Enum.map(doc.functions, & &1.id) == ["fixture:A.x/0"]
      assert [%{kind: "empty_root", message: "missing has no .ex/.exs files" <> _}] = doc.notes
    end

    test "symlinked directories are noted once and never followed" do
      root = tmp_root(%{"lib/a.ex" => "defmodule A do\nend\n"})
      File.ln_s!(Path.join(root, "lib"), Path.join(root, "lib/loop"))
      doc = ElixirFacts.build(repo: "fixture", root: root, source_roots: ["lib"])
      assert Enum.map(doc.modules, & &1.name) == ["A"]
      assert [%{kind: "symlink_skipped", file: "lib/loop"}] = doc.notes
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

    test "defprotocol and defimpl define their own modules" do
      doc =
        extract(~S'''
        defprotocol Shape do
          def area(shape)
        end
        defmodule Circle do
          defstruct [:r]
          def area(%Circle{r: r}), do: 3 * r * r
          defimpl Shape do
            def area(c), do: Circle.area(c)
          end
        end
        defimpl Shape, for: [Square, Rect] do
          def area(_), do: 0
        end
        defimpl String.Chars, for: Circle do
          def to_string(c), do: "circle #{c.r}"
        end
        ''')

      assert doc.notes == []

      assert Enum.map(doc.modules, & &1.name) ==
               ~w(Circle Shape Shape.Circle Shape.Rect Shape.Square String.Chars.Circle)

      # The nested defimpl does not merge into its host.
      assert %{clauses: [6], module: "fixture:Circle"} =
               Enum.find(doc.functions, &(&1.id == "fixture:Circle.area/1"))

      assert %{clauses: [2]} = Enum.find(doc.functions, &(&1.id == "fixture:Shape.area/1"))

      assert [%{target: %{module: "Circle", name: "area"}, kind: "remote"}] =
               from(doc, "Shape.Circle.area/1", "area")

      assert Enum.map(doc.functions, & &1.id) |> Enum.sort() == [
               "fixture:Circle.area/1",
               "fixture:Shape.Circle.area/1",
               "fixture:Shape.Rect.area/1",
               "fixture:Shape.Square.area/1",
               "fixture:Shape.area/1",
               "fixture:String.Chars.Circle.to_string/1"
             ]
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

    test "a later import of the same module replaces the earlier one" do
      doc =
        extract(~S'''
        defmodule A do
          def first(x), do: x
          def length(_), do: 99
        end
        defmodule Caller do
          import A
          import A, only: [first: 1]
          def run(x), do: {length(x), first(x)}
        end
        ''')

      assert [%{target: %{module: "Kernel"}, kind: "import"}] =
               from(doc, "Caller.run/1", "length")

      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "Caller.run/1", "first")
    end

    test "only: :functions excludes macros and only: :macros excludes functions" do
      doc =
        extract(~S'''
        defmodule M do
          defmacro length(_), do: 99
          def fun(x), do: x
        end
        defmodule Funs do
          import M, only: :functions
          def run(x), do: {length(x), fun(x)}
        end
        defmodule Macs do
          import M, only: :macros
          def run(x), do: {length(x), fun(x)}
        end
        ''')

      assert [%{target: %{module: "Kernel"}, kind: "import"}] = from(doc, "Funs.run/1", "length")
      assert [%{target: %{module: "M"}, kind: "import"}] = from(doc, "Funs.run/1", "fun")
      assert [%{target: %{module: "M"}, kind: "import"}] = from(doc, "Macs.run/1", "length")
      assert [%{target: t, kind: "local_or_import"}] = from(doc, "Macs.run/1", "fun")
      refute Map.has_key?(t, :module)
    end

    test "except is relative to the earlier import; sigils, Kernel and scope follow Elixir" do
      doc =
        extract(~S'''
        defmodule A do
          def first(x), do: x
          def second(x), do: x
          def length(_), do: 99
          def _hidden(x), do: x
          def sigil_q(s, _), do: s
          defmacro mac(x), do: x
        end
        defmodule Rel do
          import A, only: [first: 1, second: 1]
          import A, except: [second: 1]
          def run(x), do: {first(x), second(x)}
        end
        defmodule Back do
          import A, only: :functions
          import A, except: [first: 1]
          def run(x), do: {first(x), second(x), mac(x)}
        end
        defmodule Sig do
          import A, only: :sigils
          def run(x), do: {sigil_q(x, []), first(x), _hidden(x)}
        end
        defmodule NoKernel do
          import Kernel, except: [length: 1]
          import A, only: [length: 1]
          def run(x), do: length(x)
        end
        defmodule KernelMacros do
          import Kernel, only: :macros
          def run(x), do: {length(x), to_string(x)}
        end
        defmodule Scoped do
          def run(x) do
            import A, only: [first: 1]
            first(x)
          end
          def other(x), do: first(x)
        end
        defmodule Dyn do
          import A, only: [first: 1]
          import A, @opts
          def run(x), do: {first(x), length(x)}
        end
        ''')

      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "Rel.run/1", "first")
      assert [%{kind: "local_or_import"}] = from(doc, "Rel.run/1", "second")

      assert [%{kind: "local_or_import"}] = from(doc, "Back.run/1", "first")
      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "Back.run/1", "second")
      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "Back.run/1", "mac")

      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "Sig.run/1", "sigil_q")
      assert [%{kind: "local_or_import"}] = from(doc, "Sig.run/1", "first")
      assert [%{kind: "local_or_import"}] = from(doc, "Sig.run/1", "_hidden")

      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "NoKernel.run/1", "length")

      assert [%{kind: "local_or_import"}] = from(doc, "KernelMacros.run/1", "length")

      assert [%{target: %{module: "Kernel"}, kind: "import"}] =
               from(doc, "KernelMacros.run/1", "to_string")

      assert [%{target: %{module: "A"}, kind: "import"}] = from(doc, "Scoped.run/1", "first")
      assert [%{kind: "local_or_import"}] = from(doc, "Scoped.other/1", "first")

      # Non-literal options: neither A nor Kernel can be claimed.
      assert [%{kind: "local_or_import"}] = from(doc, "Dyn.run/1", "first")
      assert [%{kind: "local_or_import"}] = from(doc, "Dyn.run/1", "length")
    end
  end
end
