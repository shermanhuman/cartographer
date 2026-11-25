# Run: mise exec -- elixir plugins/extract-elixir/test/extract_test.exs
Code.require_file("../heex_tokenizer.exs", __DIR__)
Code.require_file("../heex.exs", __DIR__)
Code.require_file("../source.exs", __DIR__)
Code.require_file("../deps.exs", __DIR__)
Code.require_file("../http.exs", __DIR__)
Code.require_file("../facts.exs", __DIR__)
Code.require_file("support/hex_fixture.exs", __DIR__)
Code.require_file("support/stub_libs.exs", __DIR__)
ExUnit.start()

defmodule Cartographer.ElixirExtractTest do
  use ExUnit.Case, async: true

  alias Cartographer.ElixirFacts

  @plugin Path.expand("..", __DIR__)
  @fixture Path.expand("fixture", __DIR__)
  @schema Path.expand("schema.json", __DIR__)
  @web Path.expand("web_fixture", __DIR__)

  # The fixture project, copied to a scratch root beside the stub libraries.
  defp fixture_doc(opts \\ []) do
    root = Path.join(System.tmp_dir!(), "extract-fixture-#{System.unique_integer([:positive])}")
    File.cp_r!(@fixture, root)
    on_exit(fn -> File.rm_rf!(root) end)
    Cartographer.StubLibs.install(root)
    build(Keyword.merge([repo: "shop", root: root, source_roots: ["lib", "test"]], opts))
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

    Cartographer.StubLibs.install(root)
  end

  defp atom_exists?(name) do
    _ = String.to_existing_atom(name)
    true
  rescue
    ArgumentError -> false
  end

  # Every build reads the stub library package (test/support/stub_libs.exs)
  # from the root's own Hex cache.
  defp build(opts) do
    root = Keyword.fetch!(opts, :root)
    ElixirFacts.build(Keyword.put_new(opts, :hex_cache, Path.join(root, ".hex")))
  end

  defp extract(source) do
    build(repo: "fixture", root: tmp_root(%{"lib/scope.ex" => source}))
  end

  defp from(doc, function, name) do
    doc.calls
    |> Enum.filter(&(&1.from == "fixture:" <> function and &1.target.name == name))
    |> Enum.sort_by(& &1.line)
  end

  describe "fixture project" do
    test "inventories files, modules and namespaces" do
      doc = fixture_doc()
      assert doc.facts_version == 3

      assert doc.producer == %{
               name: "extract-elixir",
               version: "0.5.0",
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

      # A dependency's function is external.
      # map-coverage-2: only Ecto.Query exports from/2, so the owner is named.
      assert [%{target: %{module: "Ecto.Query"}, kind: "external"}] =
               calls_from(doc, "Shop.Orders.Order.recent_query/0", "from")

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

      doc = build(repo: "fixture", root: root, schema_facts: @schema)

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

      # The web fixtures too (routes, refs, stats).
      for repo <- ["admin", "rosearch"] do
        web_args = [
          Path.join(@plugin, "extract.exs"),
          "--repo",
          repo,
          "--root",
          Path.join(@web, repo),
          "--source-roots",
          "lib"
        ]

        {a, 0} = System.cmd("elixir", web_args)
        {b, 0} = System.cmd("elixir", web_args)
        assert a == b, "#{repo}: two runs differ"
      end
      decoded = JSON.decode!(first)
      assert decoded["revision"] == "abc123"
      assert String.starts_with?(first, ~s({"facts_version":3,"producer":))
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

  # Three repositories copied in shape from real ones: an admin app's
  # Phoenix routers, a client app's Req wrapper, and a shared library whose
  # one request is built across functions.
  describe "facts v2: routes, HTTP references and stats" do
    defp web_doc(repo, roots \\ ["lib"]),
      do: repo |> web(roots) |> elem(0)

    defp web(repo, roots \\ ["lib"]),
      do:
        ElixirFacts.build_with_skips(repo: repo, root: Path.join(@web, repo), source_roots: roots)

    defp skips(doc_and_skips), do: doc_and_skips |> elem(1) |> Enum.map(&{&1.line, &1.reason})

    defp stats(doc) do
      [%{language: "elixir"} = s] = doc.stats
      s
    end

    test "Phoenix verb routes join nested scope paths and aliases to a handler" do
      doc = web_doc("admin")

      assert Enum.sort(Enum.map(doc.routes, &{&1.id, &1[:handler]})) == Enum.sort([
               {"admin:route:AdminWeb.Router:GET /", "admin:AdminWeb.PageController.home/2"},
               {"admin:route:AdminWeb.Router:GET /api/v1/tenant/resolve/:slug",
                "admin:AdminWeb.API.V1.AuthController.resolve_tenant/2"},
               {"admin:route:AdminWeb.Router:GET /api/v2/tenants/:slug/users",
                "admin:AdminWeb.API.V2.TenantUsersController.index/2"},
               {"admin:route:AdminWeb.Router:GET /missing", nil},
               {"admin:route:AdminWeb.InternalRouter:POST /api/v1/internal/invites",
                "admin:AdminWeb.API.V1.BetaInviteController.create/2"},
               {"admin:route:AdminWeb.Router:POST /api/v1/leads",
                 "admin:AdminWeb.API.V1.LeadController.create/2"},
                {"admin:route:AdminWeb.InternalRouter:POST /api/v1/leads",
                "admin:AdminWeb.API.V1.LeadController.create/2"},
               {"admin:route:AdminWeb.Router:POST /api/v1/sales/cold/check",
                "admin:AdminWeb.API.V1.SalesColdController.check/2"},
               {"admin:route:AdminWeb.Router:POST /api/v1/sales/cold/suppress",
                "admin:AdminWeb.API.V1.SalesColdController.suppress/2"},
               {"admin:route:AdminWeb.Router:POST /users/log-in", "admin:AdminWeb.UserSessionController.create/2"},
               {"admin:route:AdminWeb.Router:GET /plain/health", "admin:HealthController.show/2"}
             ])

      assert %{
               method: "POST",
               path: "/api/v1/sales/cold/check",
               precision: "precise",
               file: "lib/admin_web/router.ex",
               line: 27
             } = Enum.find(doc.routes, &(&1.path == "/api/v1/sales/cold/check"))

      # No handler in this document: no handler and no precision.
      refute Map.has_key?(Enum.find(doc.routes, &(&1.path == "/missing")), :precision)

      assert [%{kind: "duplicate_route", file: "lib/admin_web/router.ex"}] =
               Enum.filter(doc.notes, &(&1.kind == "duplicate_route"))

      # live_dashboard, forward, live, match, resources (only: or not) and
      # a non-literal path are counted; pipe_through and pipelines are not
      # routes.
      assert %{not_extracted: 6, dispatch: 0, refs: 0} = stats(doc)

      assert skips(web("admin")) == [
               {43, "live_dashboard"},
               {44, "forward"},
               {50, "live"},
               {57, "get with a non-literal path"},
               {58, "match"},
               {65, "resources"}
             ]

      refute Map.has_key?(doc, :http_refs)
    end

    test "calls to a Req wrapper with a literal or interpolated path become refs" do
      doc = web_doc("rosearch")

      assert Enum.map(doc.http_refs, &{&1[:method], &1.path, &1.from, &1.line}) == [
               {"GET", "/auth/me/preferences", "rosearch:Rosearch.VehicleList.preferences/1", 5},
               {"GET", "/tenants/:p1/vehicle-cleanup", "rosearch:Rosearch.VehicleList.cleanup/3",
                9},
               # A literal path piped into the wrapper.
               {"POST", "/tenants/:p1/vehicle-cleanup/wont-fix",
                "rosearch:Rosearch.VehicleList.wont_fix/3", 14},
               # fetch/2 passes its path to get/2.
               {"GET", "/vehicles/supported-makes", "rosearch:Rosearch.VehicleList.makes/1", 22},
               {"DELETE", "/tenants/:p1/users/:p2", "rosearch:Rosearch.VehicleList.remove_user/3",
                25},
               {"GET", "/vehicles/year-ranges", "rosearch:Rosearch.VehicleList.year_ranges/2", 29}
             ]

      assert Enum.all?(doc.http_refs, &(&1.base == "Rosearch.ApiClient"))
      # The path built by path/1 (boilerplate).
      assert %{not_extracted: 1} = stats(doc)
      assert skips(web("rosearch")) == [{19, "not a literal path"}]
      refute Map.has_key?(doc, :routes)
    end

    test "a long pipe chain records each call once" do
      # Raw walker output, before the document de-duplicates: one record
      # per call site, not 2^n.
      steps = Enum.map_join(1..12, "\n", &"    |> assign(:k#{&1}, #{&1})")
      text = "defmodule P do\n  def m(s) do\n    s\n#{steps}\n  end\nend\n"
      parsed = Cartographer.ElixirSource.parse(Cartographer.ElixirSource.new(), text, "lib/p.ex", "fixture")
      assert Enum.count(parsed.calls, &(&1.name == "assign")) == 12
    end

    test "a request's base is the host behind its base_url literal" do
      doc =
        extract(~S"""
        defmodule Pay do
          @api "https://api.stripe.com/v1"
          def charge(id), do: post("/charges/#{id}")
          defp post(path), do: Req.post(Req.new(base_url: @api), url: path)
        end
        defmodule Llm do
          def ask(opts), do: call("/chat/completions", opts)
          defp call(path, opts), do: Req.post(Req.new(base_url: base_url(opts)), url: path)
          defp base_url(opts), do: Keyword.get(opts, :base_url, "https://api.openai.com/v1")
        end
        defmodule Bank do
          def list(kind), do: get("/accounts", kind)
          defp get(path, kind) do
            {base, _key} =
              case kind do
                :a -> {"https://api.stripe.com/v1", :k1}
                :b -> {"https://api.mercury.com/api/v1", :k2}
              end
            Req.get(Req.new(base_url: base), url: path)
          end
        end
        defmodule Conf do
          def get_it(id), do: get("/things/#{id}")
          defp get(path), do: Req.get(Req.new(base_url: Conf.Config.base_url(:x)), url: path)
        end
        """)

      refs = Map.new(doc.http_refs, &{&1.from, {&1.base, &1.path}})
      assert refs["fixture:Pay.charge/1"] == {"https://api.stripe.com", "/v1/charges/:p1"}
      assert refs["fixture:Llm.ask/1"] == {"https://api.openai.com", "/v1/chat/completions"}
      # A base bound to a variable (here from a case of tuples) is not read
      # (map-coverage-2 decision 2).
      assert refs["fixture:Bank.list/1"] == {"Bank", "/accounts"}
      # Nothing literal: the module, as before.
      assert refs["fixture:Conf.get_it/1"] == {"Conf", "/things/:p1"}
    end

    # Round 1, H1-M3 and H1-M4.
    test "a base is read per function, never from interpolation or unrelated literals" do
      doc =
        extract(~S"""
        defmodule Mixed do
          def pay(id), do: stripe("/charges/#{id}")
          def check(id), do: admin("/api/check/#{id}")
          defp stripe(path), do: Req.post(Req.new(base_url: "https://api.stripe.com/v1"), url: path)
          defp admin(path), do: Req.post(Req.new(base_url: "http://admin.breakdown.svc:4000"), url: path)
        end
        defmodule Interp do
          def go(ns), do: get("/x", ns)
          defp get(path, ns), do: Req.get(Req.new(base_url: "http://admin.#{ns}.svc:4000"), url: path)
        end
        defmodule Header do
          def go, do: get("/check")
          defp get(path) do
            base = Application.get_env(:app, :url)
            Req.get(Req.new(base_url: base, headers: [{"referer", "https://docs.example.com/help"}]), url: path)
          end
        end
        defmodule Direct do
          @api "https://api.stripe.com/v1"
          def charges, do: Req.get!(base_url: @api, url: "/charges")
          def refunds, do: Req.post(Req.new(base_url: "https://api.stripe.com/v1"), url: "/refunds")
        end
        """)

      refs = Map.new(doc.http_refs, &{&1.from, {&1.base, &1.path}})
      assert refs["fixture:Mixed.pay/1"] == {"https://api.stripe.com", "/v1/charges/:p1"}
      assert refs["fixture:Mixed.check/1"] == {"http://admin.breakdown.svc:4000", "/api/check/:p1"}
      assert refs["fixture:Interp.go/1"] == {"Interp", "/x"}
      assert refs["fixture:Header.go/0"] == {"Header", "/check"}
      assert refs["fixture:Direct.charges/0"] == {"https://api.stripe.com", "/v1/charges"}
      assert refs["fixture:Direct.refunds/0"] == {"https://api.stripe.com", "/v1/refunds"}
    end

    # A Req built in a helper: the module's one host.
    test "a function with no base_url of its own takes its module's one host" do
      doc =
        extract(~S"""
        defmodule Helper do
          @api "https://api.stripe.com/v1"
          def charge(id), do: post("/charges/#{id}")
          defp post(path), do: Req.post(client(), url: path)
          defp client, do: Req.new(base_url: @api)
        end
        """)

      assert [%{base: "https://api.stripe.com", path: "/v1/charges/:p1"}] = doc.http_refs
    end

    # Round 2, H2-M7: a function's own non-literal base never takes the
    # module's host.
    test "a base that isn't a literal never takes the module's one host" do
      doc =
        extract(~S"""
        defmodule Two do
          @stripe "https://api.stripe.com/v1"
          def pay(id), do: stripe("/charges/#{id}")
          def check(id), do: admin("/api/check/#{id}")
          def ping(ns), do: interp("/ping", ns)
          defp stripe(path), do: Req.post(Req.new(base_url: @stripe), url: path)
          defp admin(path), do: Req.get(Req.new(base_url: Application.fetch_env!(:app, :admin_url)), url: path)
          defp interp(path, ns), do: Req.get(Req.new(base_url: "http://admin.#{ns}.svc:4000"), url: path)
          def either(kind), do: mixed("/e", kind)
          def param(url), do: from_param("/p", url)
          def lit_case, do: lc("/c")
          def concat, do: cc("/k")
          def pair, do: tp("/t")
          def by_param(base), do: bp("/bp", base)
          def by_case(x), do: bc("/bc", x)
          def by_with(x), do: bw("/bw", x)
          def by_fn(base), do: bf("/bf", base)

          defp bp(path, base) do
            Req.get(Req.new(base_url: base), url: path)
            base = "https://api.stripe.com/v1"
            Req.get(Req.new(base_url: base), url: path)
          end

          defp bc(path, x) do
            base = "https://api.stripe.com/v1"
            case x do
              {:ok, base} -> Req.get(Req.new(base_url: base, headers: [{"a", "https://api.stripe.com"}]), url: path)
            end
          end

          defp bw(path, x) do
            with {:ok, base} <- x, do: Req.get(Req.new(base_url: base, headers: [{"a", "https://api.stripe.com"}]), url: path)
          end

          defp bf(path, base) do
            f = fn -> base = "https://api.stripe.com/v1"; base end
            _ = f
            Req.get(Req.new(base_url: base), url: path)
          end

          defp from_param(path, url) do
            base = if url, do: url, else: "https://api.stripe.com/v1"
            Req.get(Req.new(base_url: base), url: path)
          end

          defp lc(path) do
            base =
              case Application.get_env(:app, :mode) do
                :live -> "https://api.stripe.com/v1"
                _ -> "https://api.stripe.com/v1"
              end

            Req.get(Req.new(base_url: base), url: path)
          end

          defp cc(path) do
            base = "https://api.stripe.com" <> "/v1"
            Req.get(Req.new(base_url: base), url: path)
          end

          defp tp(path) do
            {base, _key} = {"https://api.stripe.com/v1", Application.fetch_env!(:app, :key)}
            Req.get(Req.new(base_url: base), url: path)
          end

          defp mixed(path, kind) do
            base =
              case kind do
                :pay -> "https://api.stripe.com/v1"
                _ -> Application.fetch_env!(:app, :admin_url)
              end

            Req.get(Req.new(base_url: base), url: path)
          end
        end
        """)

      refs = Map.new(doc.http_refs, &{&1.from, {&1.base, &1.path}})
      assert refs["fixture:Two.pay/1"] == {"https://api.stripe.com", "/v1/charges/:p1"}
      assert refs["fixture:Two.check/1"] == {"Two", "/api/check/:p1"}
      assert refs["fixture:Two.ping/1"] == {"Two", "/ping"}
      # A binding with a literal branch and a config branch (H3-m12).
      assert refs["fixture:Two.either/1"] == {"Two", "/e"}
      # map-coverage-2 decision 2: a base held in a variable is never read,
      # whatever it was bound to.
      for f <- ~w(param/1 lit_case/0 concat/0 pair/0) do
        assert {"Two", _} = refs["fixture:Two.#{f}"], f
      end
      # H5-M11: a name bound other than by a top-level `=` has no base.
      for f <- ~w(by_param/1 by_case/1 by_with/1 by_fn/1) do
        assert {"Two", _} = refs["fixture:Two.#{f}"], f
      end
    end

    # Decision 26.
    test "a loopback base_url sets no base" do
      doc =
        extract(~S"""
        defmodule Local do
          def go, do: get("/check")
          defp get(path), do: Req.get(Req.new(base_url: "http://localhost:4000/api/v1"), url: path)
        end
        """)

      assert [%{base: "Local", path: "/check"}] = doc.http_refs
    end

    test "a url: that reaches Req.new through a pipe is counted, not extracted" do
      {doc, skipped} = web("sites", ["shared-libs/lib"])
      refute Map.has_key?(doc, :http_refs)
      # create_lead's pipe into attempt/2, and Turnstile's Req.post of a
      # request whose URL is a module attribute.
      assert %{not_extracted: 2} = stats(doc)

      assert Enum.map(skipped, &{&1.file, &1.line, &1.reason}) == [
               {"shared-libs/lib/shared/admin_client.ex", 17, "piped"},
               {"shared-libs/lib/shared/turnstile.ex", 9, "Req.post without a path"}
             ]
    end

    test "a path written in a helper, or as a literal url: beside a request, is a ref there" do
      {doc, skipped} =
        ElixirFacts.build_with_skips(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/billing.ex" => ~S"""
              defmodule Billing do
                def cancel(id), do: delete("/subscriptions/#{id}")
                def list(collection), do: get("/" <> collection)
                def refresh(req), do: Req.post(req, url: "/oauth/token", body: "x")
                def show(id) do
                  path = "/subscriptions/#{id}"
                  get(path)
                end
                defp get(path), do: request(:get, path)
                defp delete(path), do: request(:delete, path)
                defp request(method, path), do: Req.request(Req.new(base_url: "https://x"), method: method, url: path)
              end
              defmodule Caller do
                def go, do: Billing.list("subscriptions")
                def view(id), do: Billing.show(id)
              end
              """
            })
        )

      assert Enum.map(doc.http_refs, &{&1[:method], &1.path, &1.from}) == [
               {"DELETE", "/subscriptions/:p1", "fixture:Billing.cancel/1"},
               {"POST", "/oauth/token", "fixture:Billing.refresh/1"}
             ]

      # show/1's id is a path parameter, not the path: the request starts in
      # show/1 (a variable path, counted), and Caller.view/1 is no request.
      # "/" <> collection names no path, so list/1 carries it and its caller
      # passes "subscriptions": counted there.
      assert %{not_extracted: 2} = stats(doc)

      assert Enum.map(skipped, &{&1.line, &1.reason}) == [
               {7, "not a literal path"},
               {14, "not a literal path"}
             ]
    end

    test "stats count call sites as in-repo, external or unresolved" do
      doc =
        extract(~S"""
        defmodule A do
          def a(x), do: x |> b() |> Enum.map(&B.c/1)
          def b(x), do: {B.c(x), length(x)}
          def d(f), do: {f.(1), unknown(2), B.missing(3)}
        end
        defmodule B do
          def c(x), do: x
        end
        """)

      # in-repo: b/1, B.c/1 (twice, once captured); external: Enum.map/2,
      # Kernel.length/1; unresolved: f.(), unknown/1, B.missing/1.
      assert stats(doc) == %{
               language: "elixir",
               call_sites: 8,
               in_repo: 3,
               dispatch: 0,
               external: 2,
               unresolved: 3,
               refs: 0,
               not_extracted: 0
             }
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
      doc = build(repo: "shop", root: root)
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

      doc = build(repo: "fixture", root: root)
      assert [%{kind: "parse_error", file: "lib/bad.ex", message: "line " <> _}] = doc.notes
      assert Enum.map(doc.functions, & &1.id) == ["fixture:Good.ok/0"]
      assert Enum.any?(doc.files, &(&1.path == "lib/bad.ex"))
    end

    test "symlinked sources are skipped with a note" do
      outside = tmp_root(%{"secret.ex" => "defmodule Secret do\n  def x, do: 1\nend\n"})
      root = tmp_root(%{"lib/real.ex" => "defmodule Real do\nend\n"})
      File.ln_s!(Path.join(outside, "secret.ex"), Path.join(root, "lib/link.ex"))
      doc = build(repo: "fixture", root: root)
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

      doc = build(repo: "fixture", root: root, schema_facts: @schema)
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
      doc = build(repo: "fixture", root: tmp_root(%{"lib/many.ex" => source}))
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
      doc = build(repo: "fixture", root: root, source_roots: ["lib", "missing"])
      assert Enum.map(doc.functions, & &1.id) == ["fixture:A.x/0"]
      assert [%{kind: "empty_root", message: "missing has no .ex/.exs files" <> _}] = doc.notes
    end

    test "symlinked directories are noted once and never followed" do
      root = tmp_root(%{"lib/a.ex" => "defmodule A do\nend\n"})
      File.ln_s!(Path.join(root, "lib"), Path.join(root, "lib/loop"))
      doc = build(repo: "fixture", root: root, source_roots: ["lib"])
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

  describe "external calls (library imports, use-generated functions)" do
    test "a library import is external; one owner is named, several are not" do
      doc =
        extract(~S"""
        defmodule One do
          import Ecto.Query
          def q, do: from(x in "t", select: x)
        end
        defmodule Two do
          use Phoenix.LiveView
          import Phoenix.HTML
          def m(s), do: {assign(s, :a, 1), lib_only(s)}
        end
        defmodule Bare do
          def n(x), do: not_anywhere(x)
        end
        """)

      assert [%{target: %{module: "Ecto.Query"}, kind: "external"}] = from(doc, "One.q/0", "from")
      # map-coverage-2: the owner is the library whose source exports it,
      # here through LiveView's quote importing Phoenix.Component.
      assert [%{target: %{module: "Phoenix.Component"}, kind: "external"}] = from(doc, "Two.m/1", "assign")
      # Two libraries in scope export it: external, no owner named.
      assert [%{target: t, kind: "external"}] = from(doc, "Two.m/1", "lib_only")
      refute Map.has_key?(t, :module)
      # No library in scope: still unresolved.
      assert [%{kind: "local_or_import"}] = from(doc, "Bare.n/1", "not_anywhere")
      assert doc.stats |> hd() |> Map.fetch!(:external) >= 2
    end

    test "bitstring type modifiers are not calls" do
      doc =
        extract(~S"""
        defmodule Bits do
          import Bitwise
          def take(<<id::binary-size(32), rest::binary>>), do: {id, rest, byte_size(rest)}
          def head(n, bin), do: (<<h::binary-size(len(n)), _::binary>> = bin; h)
          defp len(n), do: n
        end
        """)

      assert from(doc, "Bits.take/1", "size") == []
      assert [%{kind: "import"}] = from(doc, "Bits.take/1", "byte_size")
      # A size expression is code (H1-n4).
      assert [_] = from(doc, "Bits.head/2", "len")
    end

    # Round 1, H1-M2: a standalone template sees its module's `use`s.
    test "a template's library call has the module's uses as owners" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/page_html.ex" => ~S"""
              defmodule PageHTML do
                use Phoenix.Component
                import Phoenix.HTML
                embed_templates "page_html/*"
              end
              """,
              "lib/page_html/home.html.heex" => ~S"""
              <div>{lib_only(1)}</div>
              """
            })
        )

      [call] = Enum.filter(doc.calls, &(&1.target.name == "lib_only"))
      assert call.kind == "external"
      refute Map.has_key?(call.target, :module)
    end

    test "a name a project module defines publicly is never external" do
      doc =
        extract(~S"""
        defmodule Mine do
          def count(x), do: x
        end
        defmodule Uses do
          import Ecto.Query
          def c(x), do: count(x)
        end
        """)

      assert [%{kind: "local_or_import"}] = from(doc, "Uses.c/1", "count")
    end

    test "case templates expand: helpers resolve, library calls name no wrong owner" do
      doc =
        extract(~S"""
        defmodule App.ConnCase do
          use ExUnit.CaseTemplate
          using do
            quote do
              import Plug.Conn
              import App.ConnCase
            end
          end
          def log_in_user(conn, user), do: {conn, user}
        end
        defmodule App.PageTest do
          use App.ConnCase
          def t(c), do: {log_in_user(c, 1), put_req_header(c, "a", "b")}
        end
        """)

      assert [%{target: %{module: "App.ConnCase"}, kind: "import"}] =
               from(doc, "App.PageTest.t/1", "log_in_user")

      # map-coverage-2: only Plug.Conn exports it, so it is named; ExUnit's
      # quotes (read from the runtime's sources) don't define it.
      assert [%{target: %{module: "Plug.Conn"}, kind: "external"}] =
               from(doc, "App.PageTest.t/1", "put_req_header")
    end

    # map-coverage-2 decision 27: a project `use` the walk can't expand is a
    # hole in scope; the call stays unresolved rather than being credited
    # to the library.
    test "a project use the walk can't expand keeps library calls unresolved" do
      doc =
        extract(~S"""
        defmodule App.Opaque do
          defmacro __using__(opts), do: build(opts)
          def build(_), do: nil
        end
        defmodule User do
          use App.Opaque
          import Ecto.Query
          def q, do: from(x in "t", select: x)
        end
        """)

      assert [%{kind: "local_or_import"}] = from(doc, "User.q/0", "from")
    end

    test "functions a library's use generated are external, keeping their target" do
      # One module per file, as in a real repo: a dynamic_definition note
      # covers the modules of its file.
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/repo.ex" => "defmodule App.Repo do\n  use Ecto.Repo, otp_app: :app\nend\n",
              "lib/plain.ex" => "defmodule App.Plain do\n  def here, do: 1\nend\n",
              "lib/settings.ex" =>
                "defmodule App.Settings do\n  use Ecto.Schema\n  for n <- [:a, :b] do\n    def unquote(n)(), do: 1\n  end\nend\n",
              "lib/worker.ex" =>
                "defmodule App.Worker do\n  use Oban.Worker\n  def go(x), do: {App.Repo.get_by(x, id: 1), App.Plain.gone(), App.Settings.a(), new(x)}\nend\n"
            })
        )

      assert [%{target: %{module: "App.Repo", name: "get_by"}, kind: "external"}] =
               from(doc, "App.Worker.go/1", "get_by")

      assert [%{target: %{module: "App.Plain"}, kind: "remote"}] = from(doc, "App.Worker.go/1", "gone")
      assert [%{target: %{module: "App.Settings"}, kind: "remote"}] = from(doc, "App.Worker.go/1", "a")
      # The same generated function called bare inside the using module.
      assert [%{target: %{module: "Oban.Worker"}, kind: "external"}] =
               from(doc, "App.Worker.go/1", "new")
    end

    # Round 2, H2-M8: the definitions a `use` applies, from the expansion.
    test "a quote's definitions are the using module's, through dispatch, helpers and defaults" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/app_web.ex" => ~S"""
              defmodule AppWeb do
                def live_view do
                  quote do
                    use Phoenix.LiveView
                    def ok(socket), do: {:ok, socket}
                    unquote(helpers())
                  end
                end

                defp helpers do
                  quote do
                    def touch(x, opts \\ []), do: {x, opts}
                    defdelegate fmt(x), to: App.Format
                  end
                end

                defmacro __using__(which) when is_atom(which), do: apply(__MODULE__, which, [])
              end
              """,
              "lib/page.ex" => ~S"""
              defmodule App.Page do
                use AppWeb, :live_view
                import Ecto.Changeset
                def mount(_, _, s), do: {ok(s), touch(s), fmt(s), cast(s, %{}, [])}
              end
              """,
              "lib/other.ex" => ~S"""
              defmodule App.Other do
                def go(x), do: App.Page.touch(x)
              end
              """
            })
        )

      for name <- ~w(ok touch fmt) do
        assert [%{kind: "local_or_import"}] = from(doc, "App.Page.mount/3", name), name
      end

      assert [%{kind: "external"}] = from(doc, "App.Page.mount/3", "cast")
      assert [%{kind: "remote"}] = from(doc, "App.Other.go/1", "touch")
    end

    # Round 3, H3-M9: a definition the reader can't place marks the
    # module dynamic rather than being ignored.
    test "a quoted def inside if or for keeps the using module's calls unresolved" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/cond.ex" => ~S"""
              defmodule App.Cond do
                defmacro __using__(_) do
                  quote do
                    if true do
                      def touch(x), do: x
                    end
                  end
                end
              end
              """,
              "lib/loop.ex" => ~S"""
              defmodule App.Loop do
                defmacro __using__(_) do
                  quote do
                    for n <- [:a, :b], do: def unquote(n)(), do: 1
                  end
                end
              end
              """,
              "lib/user.ex" => ~S"""
              defmodule App.User do
                use App.Cond
                use App.Loop
                import Ecto.Changeset
                def go(x), do: {touch(x), a()}
              end
              """
            })
        )

      assert [%{kind: "local_or_import"}] = from(doc, "App.User.go/1", "touch")
      assert [%{kind: "local_or_import"}] = from(doc, "App.User.go/1", "a")
    end

    test "a use chain past the expansion depth marks the module dynamic" do
      chain =
        for i <- 1..4, into: %{} do
          next = if i < 4, do: "use App.U#{i + 1}", else: "def touch(x), do: x"
          {"lib/u#{i}.ex",
           "defmodule App.U#{i} do\n  defmacro __using__(_) do\n    quote do\n      #{next}\n    end\n  end\nend\n"}
        end

      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(
              Map.put(chain, "lib/deep.ex", "defmodule App.Deep do\n  use App.U1\n  import Ecto.Changeset\n  def go(x), do: touch(x)\nend\n")
            )
        )

      assert [%{kind: "local_or_import"}] = from(doc, "App.Deep.go/1", "touch")
    end

    # Round 4, H4-M10 and H4-n10.
    test "a private function a use defines is the caller's own; defimpl in a quote isn't a def" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/priv.ex" => ~S"""
              defmodule App.Priv do
                defmacro __using__(_) do
                  quote do
                    defp touch(x), do: x
                    defimpl String.Chars do
                      def to_string(_), do: "x"
                    end
                  end
                end
              end
              """,
              "lib/user.ex" => ~S"""
              defmodule App.User do
                use App.Priv
                import Ecto.Changeset
                def go(x), do: {touch(x), cast(x, %{}, [])}
              end
              """
            })
        )

      assert [%{kind: "local_or_import"}] = from(doc, "App.User.go/1", "touch")
      assert [%{kind: "external"}] = from(doc, "App.User.go/1", "cast")
    end

    # map-coverage-2, step 4.
    test "on evidence: collisions, query vocabulary, unknown modules, holes and super" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/mine.ex" => "defmodule Mine do\n  def change(a, b), do: {a, b}\n  def count(x), do: x\nend\n",
              "lib/user.ex" => ~S"""
              defmodule App.User do
                import Ecto.Changeset
                import Ecto.Query
                def c(x), do: change(x, %{})
                def count(q), do: q
                def q(id, f), do: from(u in project_source(), where: count(u.id) > ^f.(id) and fragment("x") == 1)
                def project_source, do: "users"
                def far(x), do: Some.Unindexed.thing(x)
                def known(x), do: Enum.map(x, & &1)
              end
              """,
              "lib/schema.ex" => ~S"""
              defmodule App.Schema do
                use Ecto.Schema
                for n <- [:field] do
                  def unquote(n)(x), do: x
                end
                def go(x), do: field(x)
              end
              """,
              "lib/sup.ex" => "defmodule App.Sup do\n  use Phoenix.LiveView\n  def mount(a, b), do: super(a, b)\nend\n"
            })
        )

      # Mine.change/2 is out of scope; Ecto.Changeset's source exports it.
      assert [%{kind: "external", target: %{module: "Ecto.Changeset"}}] = from(doc, "App.User.c/1", "change")
      # Query vocabulary inside a proven Ecto.Query macro, before the
      # project's own count/1 (decision 25).
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "App.User.q/2", "count")
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "App.User.q/2", "fragment")
      # Not vocabulary: a project call inside the query stays a project edge.
      assert [%{kind: "local"}] = from(doc, "App.User.q/2", "project_source")
      # A module nothing knows: unresolved, not external.
      assert [%{kind: "remote"}] = from(doc, "App.User.far/1", "thing")
      doc_stats = Enum.find(doc.stats, &(&1.language == "elixir"))
      assert doc_stats.unresolved >= 1
      # Enum is known to the runtime.
      assert [%{kind: "remote", target: %{module: "Enum"}}] = from(doc, "App.User.known/1", "map")
      # A computed def is a hole: field/1 isn't credited to Ecto.Schema.
      assert [%{kind: "local_or_import"}] = from(doc, "App.Schema.go/1", "field")
      # super(...) is not a call.
      assert from(doc, "App.Sup.mount/2", "super") == []
    end

    # Review round 1, X1-m1: each rule pinned by its own case.
    test "the evidence rules, one case each" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/repo.ex" => "defmodule App.Repo do\n  use Ecto.Repo, otp_app: :app\nend\n",
              "lib/own.ex" => "defmodule App.OwnRepo do\n  use Ecto.Repo, otp_app: :app\n  def all(q, _opts), do: q\nend\n",
              "lib/mystery.ex" => "defmodule App.Mystery do\n  def from(a, b), do: {a, b}\nend\n",
              "lib/a.ex" => ~S"""
              defmodule A.UnreadableImport do
                import Ecto.Query
                import App.Mystery, only: opts()
                def opts, do: []
                def go(x), do: from(x, [])
              end
              defmodule A.UnknownModule do
                import Missing.Lib
                import Ecto.Changeset
                def go(x), do: change(x)
              end
              defmodule A.UseWithoutQuotes do
                use :lists
                import Ecto.Changeset
                def go(x), do: change(x)
              end
              defmodule A.LeakyUse do
                use Leaky.Lib
                import Ecto.Changeset
                def go(x), do: change(x)
              end
              defmodule A.NotAdmitted do
                import Ecto.Changeset, only: [cast: 3]
                def go(x), do: change(x)
              end
              defmodule A.WrongArity do
                def go(x), do: App.Repo.all(x, 1, 2, 3)
              end
              defmodule A.OwnDefinition do
                def go(x), do: App.OwnRepo.all(x, [])
              end
              defmodule A.NoImport do
                def count(x), do: x
                def where(q, b, e), do: {q, b, e}
                def go(q), do: where(q, [], count(1))
              end
              defmodule A.Deep do
                import Ecto.Query
                def go(q), do: where(q, [u], not is_nil(count(u.id)))
              end
              """
            })
        )

      for m <- ~w(UnreadableImport UnknownModule UseWithoutQuotes NotAdmitted LeakyUse) do
        assert [%{kind: "local_or_import"}] = from(doc, "A.#{m}.go/1", if(m == "UnreadableImport", do: "from", else: "change")), m
      end

      # No generated all/4.
      assert [%{kind: "remote"}] = from(doc, "A.WrongArity.go/1", "all")
      # The project module defines all/2 itself: its own, not the library's.
      assert [%{kind: "remote"}] = from(doc, "A.OwnDefinition.go/1", "all")
      # Without `import Ecto.Query`, a local where/3 is no query macro.
      assert [%{kind: "local"}] = from(doc, "A.NoImport.go/1", "count")
      # Two calls deep inside the macro is still its argument.
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "A.Deep.go/1", "count")
    end

    test "a call to a module with no evidence counts as unresolved, not external" do
      doc = extract("defmodule U do\n  def go(x), do: Some.Unindexed.thing(x)\nend\n")
      stats = Enum.find(doc.stats, &(&1.language == "elixir"))
      assert {stats.external, stats.unresolved} == {0, 1}
    end

    # Review round 1, X1-M1 and X1-m2.
    test "query vocabulary only in a query macro's later arguments" do
      doc =
        extract(~S"""
        defmodule P do
          import Ecto.Query
          def count(x), do: x
          def src(o), do: from(u in count(o), select: u)
          def first(q), do: where(count(q), [u], u.id > 1)
          def sub(q), do: subquery(count(q))
          def dyn(repo, x), do: repo.update(count(x), [])
          def dyn_later(repo, x), do: repo.update(x, [], count(1))
          def nested(ids), do: from(u in "u", preload: [posts: ^from(p in "p", where: fragment("x"))], where: u.id in ^ids)
          def later(q), do: where(q, [u], count(u.id) > 1)
          def in_exists(q), do: where(q, [u], exists(count(q)))
          def in_sub(q), do: where(q, [u], u.id in subquery(count(q)))
          def dyn_expr, do: dynamic(is_nil(1))
          def dyn_binding, do: dynamic([u], is_nil(u.x))
        end
        """)

      for f <- ~w(src/1 first/1 sub/1 dyn/2 dyn_later/2) do
        assert [%{kind: "local"}] = from(doc, "P.#{f}", "count"), f
      end

      # Review round 2 (X2-M3, X2-M4): through subquery/exists the argument
      # is code; dynamic's expression is its last argument.
      for f <- ~w(in_exists/1 in_sub/1) do
        assert [%{kind: "local"}] = from(doc, "P.#{f}", "count"), f
      end

      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "P.dyn_expr/0", "is_nil")
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "P.dyn_binding/0", "is_nil")

      # A query nested under ^ keeps its own vocabulary.
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "P.nested/1", "fragment")
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "P.later/1", "count")
    end

    # Review round 3 (X3-M5..M7): one case per row of the query-position
    # table (Ecto 3.14.3's builders, recorded with the decision).
    test "the query-position table, one row each" do
      doc =
        extract(~S"""
        defmodule T do
          import Ecto.Query
          def count(x), do: x
          defmacro coalesce(a, b, c), do: {a, b, c}
          def join_kind(q, o), do: join(q, count(o), [u], p in "p", on: count(p.id) > 0)
          def type_arg(q, o), do: select(q, [u], type(u.x, count(o)))
          def map_fields(q, o), do: select(q, [u], map(u, count(o)))
          def struct_fields(q, o), do: select(q, [u], struct(u, count(o)))
          def fragment_first(q, o), do: where(q, [u], fragment(count(o), u.x))
          def fragment_later(q), do: where(q, [u], fragment("?", count(u.id)))
          def in_any(q, o), do: where(q, [u], u.x > any(count(o)))
          def in_all(q, o), do: where(q, [u], u.x > all(count(o)))
          def in_values(q, o), do: where(q, [u], u.id in values(count(o), %{}))
          def arity(q), do: where(q, [u], coalesce(u.a, u.b, u.c))
        end
        """)

      for f <- ~w(join_kind/2 type_arg/2 map_fields/2 struct_fields/2 fragment_first/2 in_any/2 in_all/2 in_values/2) do
        assert {"local", "T"} in for(c <- from(doc, "T.#{f}", "count"), do: {c.kind, c.target[:module]}), f
        refute Enum.any?(from(doc, "T.#{f}", "count"), &(&1.kind == "external" and &1.target[:module] == "Ecto.Query.API" and f != "join_kind/2")), f
      end

      # join's on: is a query expression; fragment's later arguments too.
      assert {"external", "Ecto.Query.API"} in for(c <- from(doc, "T.join_kind/2", "count"), do: {c.kind, c.target[:module]})
      assert [%{kind: "external", target: %{module: "Ecto.Query.API"}}] = from(doc, "T.fragment_later/1", "count")
      # Matched by name and arity: coalesce/3 is the project's macro.
      assert [%{kind: "local"}] = from(doc, "T.arity/1", "coalesce")
    end

    test "under ^ a call is ordinary code, not query vocabulary" do
      doc =
        extract(~S"""
        defmodule P do
          import Ecto.Query
          def count(x), do: x
          def q(n), do: from(u in "t", where: u.n == ^count(n))
        end
        """)

      assert [%{kind: "local"}] = from(doc, "P.q/1", "count")
    end

    # Round 1, H1-m1 and H1-m2.
    test "owners respect only:, project imports that pass a library on, and captures" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/only.ex" =>
                "defmodule App.Only do\n  import Ecto.Query, only: [from: 2]\n  import Ecto.Changeset\n  def c(x), do: cast(x, %{}, [])\nend\n",
              "lib/gettext.ex" => "defmodule App.Gettext do\n  use Gettext.Backend, otp_app: :app\nend\n",
              "lib/view.ex" =>
                "defmodule App.View do\n  import Phoenix.Component\n  import App.Gettext\n  def t, do: gettext(\"hi\")\nend\n",
              "lib/repo.ex" => "defmodule App.Repo do\n  use Ecto.Repo, otp_app: :app\nend\n",
              "lib/w.ex" =>
                "defmodule App.W do\n  def go(x), do: {&App.Repo.insert/1, apply(App.Repo, :get, [x, 1])}\nend\n"
            })
        )

      assert [%{kind: "external", target: %{module: "Ecto.Changeset"}}] = from(doc, "App.Only.c/1", "cast")
      # map-coverage-2: gettext/1 arrives through the project module that
      # `use Gettext.Backend` generated it into, which is named.
      assert [%{kind: "external", target: %{module: "App.Gettext"}}] = from(doc, "App.View.t/0", "gettext")
      assert [%{kind: "capture", target: %{module: "App.Repo"}}] = from(doc, "App.W.go/1", "insert")
      assert [%{kind: "apply", target: %{module: "App.Repo"}}] = from(doc, "App.W.go/1", "get")

      stats = Enum.find(doc.stats, &(&1.language == "elixir"))
      assert stats.unresolved == 0
    end

    # Round 1, H1-M1: functions the inventory can't list stay unresolved.
    test "a function defined at runtime or in a project quote is never external" do
      doc =
        build(
          repo: "fixture",
          root:
            tmp_root(%{
              "lib/settings.ex" =>
                "defmodule App.Settings do\n  use Ecto.Schema\n  for n <- [:a, :b] do\n    def unquote(n)(), do: 1\n  end\n  def both, do: {a(), b()}\nend\n",
              "lib/reader.ex" =>
                "defmodule App.Reader do\n  import Ecto.Query\n  import App.Settings\n  def r, do: a()\nend\n",
              "lib/helper.ex" =>
                "defmodule App.Helper do\n  defmacro __using__(_) do\n    quote do\n      use Ecto.Schema\n      def touch(x), do: x\n    end\n  end\nend\n",
              "lib/thing.ex" =>
                "defmodule App.Thing do\n  use App.Helper\n  import Ecto.Changeset\n  def go(x), do: {touch(x), cast(x, %{}, [])}\nend\n",
              "lib/caller.ex" =>
                "defmodule App.Caller do\n  def go(x), do: App.Thing.touch(x)\nend\n"
            })
        )

      assert [%{kind: "local_or_import"}] = from(doc, "App.Settings.both/0", "a")
      assert [%{kind: "local_or_import"}] = from(doc, "App.Reader.r/0", "a")
      assert [%{kind: "local_or_import"}] = from(doc, "App.Thing.go/1", "touch")
      assert [%{kind: "remote"}] = from(doc, "App.Caller.go/1", "touch")
      # The helper's quoted defs don't hide the module's library calls.
      assert [%{kind: "external"}] = from(doc, "App.Thing.go/1", "cast")
    end
  end

end
