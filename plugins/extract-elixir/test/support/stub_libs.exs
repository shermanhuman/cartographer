defmodule Cartographer.StubLibs do
  @moduledoc """
  A stand-in for the Hex packages the extractor tests name (Ecto, Phoenix,
  Plug, Oban, Gettext): just the exports and `__using__` quotes the tests
  rely on. `install/1` writes it as a verified tarball into `root/.hex` and
  a `mix.lock` at `root` (unless the root has its own).
  """

  @source ~S"""
  defmodule Ecto.Query do
    defmacro from(expr, kw \\ []), do: {expr, kw}
    defmacro where(q, binding \\ [], expr), do: {q, binding, expr}
    defmacro select(q, binding \\ [], expr), do: {q, binding, expr}
    defmacro update(q, binding \\ [], expr), do: {q, binding, expr}
    def subquery(q, opts \\ []), do: {q, opts}
    defmacro dynamic(binding \\ [], expr), do: {binding, expr}
  end
  defmodule Ecto.Query.API do
    def count(v), do: v
    def fragment(f), do: f
    def ilike(a, b), do: {a, b}
    def max(v), do: v
    def is_nil(v), do: v
    def exists(q), do: q
  end
  defmodule Ecto.Changeset do
    def cast(data, params, permitted, opts \\ []), do: {data, params, permitted, opts}
    def change(data, changes \\ %{}), do: {data, changes}
    def validate_required(cs, fields), do: {cs, fields}
  end
  defmodule Ecto.Repo do
    defmacro __using__(opts) do
      quote do
        @before_compile unquote(Keyword.get(opts, :adapter))
        def all(q, opts \\ []), do: {q, opts}
        def get(q, id, opts \\ []), do: {q, id, opts}
        def get_by(q, clauses, opts \\ []), do: {q, clauses, opts}
        def insert(s, opts \\ []), do: {s, opts}
      end
    end
  end
  defmodule Ecto.Schema do
    defmacro __using__(_) do
      quote do
        import Ecto.Schema
      end
    end
    defmacro schema(source, do: block), do: {source, block}
    defmacro field(name, type \\ :string, opts \\ []), do: {name, type, opts}
  end
  defmodule Phoenix.Component do
    defmacro __using__(_) do
      quote do
        import Phoenix.Component
      end
    end
    def assign(socket, kw), do: {socket, kw}
    def assign(socket, k, v), do: {socket, k, v}
    def link(assigns), do: assigns
    def form(assigns), do: assigns
    def to_form(data, opts \\ []), do: {data, opts}
    def lib_only(x), do: x
  end
  defmodule Phoenix.LiveView do
    defmacro __using__(_) do
      quote do
        import Phoenix.LiveView
        use Phoenix.Component
      end
    end
    def push_event(socket, event, payload), do: {socket, event, payload}
  end
  defmodule Phoenix.HTML do
    def raw(x), do: x
    def lib_only(x), do: x
  end
  defmodule Leaky.Lib do
    defmacro __using__(_) do
      quote do
        use Not.Shipped
      end
    end
  end
  defmodule Plug.Conn do
    def put_req_header(conn, k, v), do: {conn, k, v}
  end
  defmodule Phoenix.ConnTest do
    @verbs [:get, :post]
    for v <- @verbs do
      defmacro unquote(v)(conn, path, params \\ nil), do: {conn, path, params}
    end
  end
  defmodule Oban.Worker do
    defmacro __using__(_) do
      quote do
        def new(args, opts \\ []), do: {args, opts}
      end
    end
  end
  defmodule Gettext.Backend do
    defmacro __using__(_) do
      quote do
        defmacro gettext(msgid, bindings \\ []), do: {msgid, bindings}
      end
    end
  end
  """

  def install(root) do
    lock = Path.join(root, "mix.lock")

    unless File.exists?(lock) do
      text = Cartographer.HexFixture.write(Path.join(root, ".hex"), [{"stub_libs", "1.0.0", %{"lib/stub.ex" => @source}}])
      File.write!(lock, text)
    end

    root
  end
end
