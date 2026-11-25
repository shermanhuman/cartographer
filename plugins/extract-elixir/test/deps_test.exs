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

  test "a lock is read as data, never evaluated" do
    root = scratch()
    File.mkdir_p!(root)
    marker = Path.join(root, "pwned")
    evil = "%{\"x\": {:hex, :x, \"1.0.0\", \"a\", [:mix], [], \"hexpm\", \"b\"}} |> tap(fn _ -> File.write!(\"" <> marker <> "\", \"\") end)"
    File.write!(Path.join(root, "mix.lock"), evil)
    assert ElixirDeps.read_lock(root, ".") == []
    refute File.exists?(Path.join(root, "pwned"))
  end
end
