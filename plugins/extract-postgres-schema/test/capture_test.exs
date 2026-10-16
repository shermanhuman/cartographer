# Run: mise exec -- elixir plugins/extract-postgres-schema/test/capture_test.exs
# Needs no database: guards and the catalog-to-facts conversion are pure.
Code.require_file("../capture_lib.exs", __DIR__)
ExUnit.start()

defmodule Cartographer.PgCaptureTest do
  use ExUnit.Case, async: true

  alias Cartographer.PgCapture

  test "only loopback hosts are allowed" do
    for host <- ["127.0.0.1", "localhost", "::1"], do: assert(PgCapture.check_host(host) == :ok)

    for host <- ["10.0.0.5", "db.internal", "0.0.0.0", "127.0.0.2", ""],
        do: assert({:error, _} = PgCapture.check_host(host))
  end

  test "database names need an allowed prefix; cartographer_ is always allowed" do
    {:ok, defaults} = PgCapture.prefixes([])
    assert defaults == ["cartographer_"]
    assert PgCapture.check_database("cartographer_shop", defaults) == :ok
    assert {:error, msg} = PgCapture.check_database("system_map_shop", defaults)
    assert msg =~ "--db-prefix"
    assert {:error, _} = PgCapture.check_database("shop_prod", defaults)

    {:ok, prefixes} = PgCapture.prefixes(["system_map_,shop_test", "other_"])
    assert prefixes == ["cartographer_", "system_map_", "shop_test", "other_"]
    assert PgCapture.check_database("system_map_x", prefixes) == :ok
    assert PgCapture.check_database("shop_test", prefixes) == :ok
    assert PgCapture.check_database("shop_test_2", prefixes) == :ok

    # Names are plain lowercase identifiers: no quoting tricks.
    for bad <- ["cartographer_X", "cartographer_a;drop", "cartographer_a b", "cartographer-a"],
        do: assert({:error, _} = PgCapture.check_database(bad, prefixes))
  end

  test "prefixes must be non-empty identifiers, so a blank prefix cannot allow every database" do
    assert {:error, _} = PgCapture.prefixes([""])
    assert {:error, _} = PgCapture.prefixes(["a,,b"])
    assert {:error, _} = PgCapture.prefixes(["Prod_"])
    assert {:error, _} = PgCapture.prefixes(["x%"])
  end

  test "parse_args runs every guard and applies defaults" do
    assert {:ok, opts} = PgCapture.parse_args(["--database", "cartographer_x"])

    assert opts == %{
             database: "cartographer_x",
             host: "127.0.0.1",
             port: 5432,
             user: "postgres",
             container: nil,
             schemas: ["public"]
           }

    assert {:ok, %{schemas: ["public", "audit"], container: "db-1"}} =
             PgCapture.parse_args([
               "--database",
               "ci_db",
               "--db-prefix",
               "ci_",
               "--schemas",
               "public,audit",
               "--container",
               "db-1"
             ])

    assert {:error, "--database is required"} = PgCapture.parse_args([])
    assert {:error, _} = PgCapture.parse_args(["--database", "prod"])

    assert {:error, _} =
             PgCapture.parse_args(["--database", "cartographer_x", "--host", "db.example.com"])

    assert {:error, _} =
             PgCapture.parse_args(["--database", "cartographer_x", "--schemas", "public');--"])

    assert {:error, _} =
             PgCapture.parse_args(["--database", "cartographer_x", "--container", "-it"])

    assert {:error, _} = PgCapture.parse_args(["--database", "cartographer_x", "--bogus", "1"])
  end

  test "version: 15+ accepted, 18 is silent, others warn, older fail" do
    assert PgCapture.check_version(180_003) == :ok
    assert {:warn, _} = PgCapture.check_version(150_008)
    assert {:warn, _} = PgCapture.check_version(170_002)
    assert {:warn, _} = PgCapture.check_version(190_000)
    assert {:error, _} = PgCapture.check_version(140_011)
  end

  test "the session must report read-only before the catalog query" do
    assert PgCapture.check_read_only("on") == :ok
    assert {:error, _} = PgCapture.check_read_only("off")
  end

  test "every psql session carries read-only and statement-timeout options" do
    opts = %{
      database: "cartographer_x",
      host: "127.0.0.1",
      port: 5433,
      user: "postgres",
      container: nil,
      schemas: ["public"]
    }

    {"psql", args, env} = PgCapture.psql_command(opts, "SELECT 1")
    assert {"PGOPTIONS", "-c default_transaction_read_only=on -c statement_timeout=30000"} in env
    assert ["--host", "127.0.0.1", "--port", "5433"] == Enum.slice(args, 1, 4)
    assert List.last(args) == "SELECT 1"
    assert "--no-password" in args

    {"docker", ["exec", "--env", "PGOPTIONS=" <> pgoptions, "db-1", "psql" | dargs], _} =
      PgCapture.psql_command(%{opts | container: "db-1"}, "SELECT 1")

    assert pgoptions == PgCapture.pgoptions()
    refute "--host" in dargs
  end

  test "catalog SQL takes the validated schema list and reads metadata only" do
    sql = PgCapture.sql(["public", "audit"])
    assert sql =~ "IN ('public','audit')"
    refute sql =~ "__SCHEMAS__"
    refute sql =~ ~r/\b(INSERT|UPDATE|DELETE|DROP|ALTER|CREATE|TRUNCATE)\b/i
  end

  test "catalog output converts to the checked-in sample facts document" do
    raw = Path.join(__DIR__, "raw_catalog.json") |> File.read!() |> JSON.decode!()
    doc = PgCapture.to_facts(raw)
    assert PgCapture.encode(doc) <> "\n" == File.read!(Path.join(__DIR__, "sample.json"))

    orders = Enum.find(doc.tables, &(&1.id == "public.orders"))

    assert Enum.map(orders.constraints, & &1.kind) == [
             "foreign_key",
             "primary_key",
             "foreign_key",
             "check"
           ]

    # relkind survives as `kind`: the view is distinguishable from tables.
    assert Map.new(doc.tables, &{&1.id, &1[:kind]}) == %{
             "public.customers" => "table",
             "public.orders" => "table",
             "public.order_totals" => "view"
           }

    customers = Enum.find(doc.tables, &(&1.id == "public.customers"))
    # NOT NULL catalog constraints (PostgreSQL 18) duplicate columns[].nullable.
    refute Enum.any?(customers.constraints, &(&1.name == "customers_email_not_null"))
    refute Map.has_key?(hd(orders.columns), :default)
    # A foreign key into an uncaptured schema becomes a note, not a dangling edge.
    assert [%{from_table: "public.orders", to_table: "public.customers"}] = doc.foreign_keys
    assert [%{kind: "uncaptured_foreign_key_target"}] = doc.notes
  end

  test "CLI refuses before connecting when a guard fails" do
    script = Path.expand("../capture.exs", __DIR__)

    {out, status} =
      System.cmd("elixir", [script, "--database", "production"], stderr_to_stdout: true)

    assert status == 2
    assert out =~ "allowed disposable prefix"
  end
end
