# Run: elixir plugins/extract-elixir/test/deps_test.exs
ExUnit.start()
Code.require_file("../deps.exs", __DIR__)
Code.require_file("support/hex_fixture.exs", __DIR__)

defmodule Cartographer.ElixirDepsTest do
  use ExUnit.Case, async: true
  alias Cartographer.{ElixirDeps, HexFixture}

  defp scratch do
    dir = Path.join(System.tmp_dir!(), "deps-test-#{System.unique_integer([:positive])}")
    on_exit(fn -> File.rm_rf!(dir) end)
    dir
  end

  @lib ~S"""
  defmodule Lib.Query do
    def from(a, b \\ []), do: {a, b}
    defmacro where(q, e), do: {q, e}
  end
  defmodule Lib.Repo do
    defmacro __using__(opts) do
      quote do
        @before_compile unquote(Keyword.get(opts, :adapter))
        import Lib.Query
        def all(q, opts \\ []), do: {q, opts}
        if true do
          def get(id), do: id
        end
      end
    end
  end
  defmodule Lib.Adapter do
    defmacro __before_compile__(_env) do
      quote do
        def query!(sql, params \\ []), do: {sql, params}
      end
    end
  end
  defmodule Lib.Hooked do
    defmacro __using__(_) do
      quote do
        @before_compile Lib.Hook
      end
    end
  end
  defmodule Lib.Hook do
    defmacro __before_compile__(_env) do
      quote do
        def hooked, do: :ok
      end
    end
  end
  defmodule Lib.Cond do
    if Code.ensure_loaded?(Plug.Test) do
      def json(conn, data), do: {conn, data}
    else
      def json(_conn, _data), do: :no_plug
    end
  end
  defmodule Lib.Error do
    defexception [:message]
  end
  defprotocol Lib.Proto do
    def to_form(data, opts)
  end
  defmodule Lib.Behaviour do
    defmacro __using__(_) do
      quote do
        def no_user_verify(opts \\ []), do: opts
      end
    end
  end
  defmodule Lib.Hasher do
    use Lib.Behaviour
  end
  defmodule Lib.Sql do
    defmacro __using__(_) do
      quote do
        def query!(sql, params \\ []), do: {sql, params}
      end
    end
  end
  defmodule Lib.Pg do
    use Lib.Sql
  end
  defmodule Lib.Other do
    defmacro __using__(_) do
      quote do
        def secret, do: :no
      end
    end
  end
  defmodule Lib.Shy do
    defmacro __using__(_) do
      quote do
        import Lib.Other
        defp hidden, do: :no
        defmodule Inner do
          def inner_only, do: :no
        end
        def shown, do: hidden()
      end
    end
  end
  defmodule Lib.Verbs do
    @verbs [:get, :post]
    for v <- @verbs do
      defmacro unquote(v)(conn, path), do: {conn, path}
    end
  end
  """

  defp fixture(opts \\ []) do
    root = scratch()
    hex = Path.join(root, "hex")
    lock = HexFixture.write(hex, [{"lib", "1.0.0", %{"lib/lib.ex" => @lib}}], opts)
    File.mkdir_p!(Path.join(root, "app/lib"))
    File.write!(Path.join(root, "app/mix.lock"), lock)
    {root, hex}
  end

  test "a verified package gives exports, and the superset reader follows use, @before_compile and option aliases" do
    {root, hex} = fixture()
    ev = ElixirDeps.load(root, ["app/lib/a.ex"], hex)
    assert ev.notes == []
    lock = ElixirDeps.lock_of(ev, "app/lib/a.ex")
    assert lock == "app"

    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Query"), "from", 1)
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Query"), "where", 2)
    # Under a module-level `if` (a superset).
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Cond"), "json", 2)
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Error"), "exception", 1)
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Proto"), "to_form", 2)
    # What a library module's own top-level `use` generates into it.
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Hasher"), "no_user_verify", 0)
    # A top-level `for` over a literal list attribute.
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Verbs"), "post", 2)

    r = ElixirDeps.reach(ev, lock, "Lib.Repo", ["Lib.Adapter"])
    names = MapSet.new(r.defs, &elem(&1, 0))
    # Under `if` counts (a superset), and the adapter's hook is reached.
    for n <- ~w(all get query!), do: assert(MapSet.member?(names, n), n)
    assert "Lib.Query" in r.imports
    assert r.unknown == []
    # A literal @before_compile is the only path to Lib.Hook.
    assert Enum.any?(ElixirDeps.reach(ev, lock, "Lib.Hooked", []).defs, &(elem(&1, 0) == "hooked"))
  end

  # Review round 1, X1-m1: the reader rules each pinned.
  test "the reader: arity ranges, imports give exports only, top-level uses, no defp or nested modules" do
    {root, hex} = fixture()
    ev = ElixirDeps.load(root, ["app/lib/a.ex"], hex)
    lock = "app"
    refute ElixirDeps.exported?(ElixirDeps.exports(ev, lock, "Lib.Query"), "from", 3)
    # A reached module's own top-level `use` is followed (the adapter shape).
    assert Enum.any?(ElixirDeps.reach(ev, lock, "Lib.Repo", ["Lib.Pg"]).defs, &(elem(&1, 0) == "query!"))
    names = ElixirDeps.reach(ev, lock, "Lib.Shy", []).defs |> Enum.map(&elem(&1, 0))
    assert "shown" in names
    # An import contributes exports, never its quotes (decision 23); a
    # quoted defp is private; a module nested in a quote is its own.
    refute "secret" in names
    refute "hidden" in names
    refute "inner_only" in names
  end

  test "the runtime knows Elixir and OTP modules; nothing else" do
    {root, hex} = fixture()
    ev = ElixirDeps.load(root, ["app/lib/a.ex"], hex)
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, "app", "Enum"), "map", 2)
    assert ElixirDeps.exported?(ElixirDeps.exports(ev, "app", ":lists"), "map", 2)
    assert ElixirDeps.exports(ev, "app", "No.Such.Module") == nil
  end

  test "a checksum mismatch or a missing tarball gives no evidence and a note naming it" do
    {root, hex} = fixture(wrong_checksum: "lib")
    ev = ElixirDeps.load(root, ["app/lib/a.ex"], hex)
    assert [%{kind: "dependency_missing", file: "lib-1.0.0", message: msg}] = ev.notes
    assert msg =~ "checksum"
    assert ElixirDeps.exports(ev, "app", "Lib.Query") == nil

    File.rm!(Path.join(hex, "lib-1.0.0.tar"))
    ev = ElixirDeps.load(root, ["app/lib/a.ex"], hex)
    assert [%{kind: "dependency_missing", message: msg}] = ev.notes
    assert msg =~ "not in the Hex cache"
  end

  test "each file takes the nearest lock; a file with none has no dependency evidence" do
    {root, hex} = fixture()
    File.mkdir_p!(Path.join(root, "other/lib"))
    ev = ElixirDeps.load(root, ["app/lib/a.ex", "other/lib/b.ex"], hex)
    assert ElixirDeps.lock_of(ev, "app/lib/a.ex") == "app"
    assert ElixirDeps.lock_of(ev, "other/lib/b.ex") == nil
    assert ElixirDeps.exports(ev, nil, "Lib.Query") == nil
  end

  # Review round 1, X1-m5 and X1-m6.
  test "evidence that goes missing says so; a lock creates no atoms" do
    root = scratch()
    File.mkdir_p!(root)
    name = "zz_never_seen_pkg_#{System.unique_integer([:positive])}"

    File.write!(Path.join(root, "mix.lock"), """
    %{
      "#{name}": {:hex, :#{name}, "1.0.0", "a", [:mix], [], "hexpm", "#{String.duplicate("b", 64)}"},
      "oban": {:hex, :oban, "2.0.0", "a", [:mix], [], "oban", "#{String.duplicate("c", 64)}"},
      "old": {:hex, :old, "0.1.0", "a", [:mix], []},
    }
    """)

    {entries, notes} = ElixirDeps.read_lock(root, ".")
    assert [%{name: ^name}] = entries
    assert_raise ArgumentError, fn -> String.to_existing_atom(name) end
    assert Enum.map(notes, & &1.file) |> Enum.sort() == ["oban-2.0.0", "old-0.1.0"]

    File.write!(Path.join(root, "mix.lock"), <<0xFF, 0xFE>>)
    assert {[], [%{file: "mix.lock"}]} = ElixirDeps.read_lock(root, ".")
    assert ElixirDeps.source_modules(<<"defmodule A do ", 0xFF, " end">>) == []
  end

  # The sample lock the Go digest's parser test reads too.
  test "the shared sample lock: one package, notes for the rest" do
    root = scratch()
    File.mkdir_p!(root)
    File.cp!(Path.join(__DIR__, "support/sample.lock"), Path.join(root, "mix.lock"))
    {entries, notes} = ElixirDeps.read_lock(root, ".")
    assert [%{name: "ecto", version: "3.14.3"}] = entries
    assert notes |> Enum.map(& &1.file) |> Enum.sort() == ["oban_pro-1.5.0", "old-0.1.0"]
  end

  test "a lock is read as data, never evaluated" do
    root = scratch()
    File.mkdir_p!(root)
    marker = Path.join(root, "pwned")
    evil = "%{\"x\": {:hex, :x, \"1.0.0\", \"a\", [:mix], [], \"hexpm\", \"b\"}} |> tap(fn _ -> File.write!(\"" <> marker <> "\", \"\") end)"
    File.write!(Path.join(root, "mix.lock"), evil)
    assert {[], [%{kind: "dependency_missing"}]} = ElixirDeps.read_lock(root, ".")
    refute File.exists?(Path.join(root, "pwned"))
  end
end
