defmodule AdminWeb.PageController do
  def home(conn, _params), do: conn
end

defmodule AdminWeb.UserSessionController do
  def create(conn, _params), do: conn
end

defmodule AdminWeb.API.V1.AuthController do
  def resolve_tenant(conn, %{"slug" => _slug}), do: conn
end

defmodule AdminWeb.API.V1.LeadController do
  def create(conn, _params), do: conn
end

defmodule AdminWeb.API.V1.SalesColdController do
  def check(conn, _params), do: conn
  def suppress(conn, _params), do: conn
end

defmodule AdminWeb.API.V1.BetaInviteController do
  def create(conn, _params), do: conn
end

defmodule AdminWeb.API.V2.TenantUsersController do
  def index(conn, _params), do: conn
end
