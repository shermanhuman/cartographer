defmodule AdminWeb.Router do
  use AdminWeb, :router

  @dynamic_path "/generated"

  pipeline :api do
    plug :accepts, ["json"]
  end

  scope "/", AdminWeb do
    pipe_through :browser

    get "/", PageController, :home
  end

  scope "/api/v1", AdminWeb.API.V1 do
    pipe_through :api

    get "/tenant/resolve/:slug", AuthController, :resolve_tenant
    post "/leads", LeadController, :create
  end

  # The sales site's cold-email questions.
  scope "/api/v1/sales/cold", AdminWeb.API.V1 do
    pipe_through [:api, :api_sales_key]

    post "/check", SalesColdController, :check
    post "/suppress", SalesColdController, :suppress
  end

  scope "/api" do
    scope "/v2", AdminWeb.API do
      get "/tenants/:slug/users",
          V2.TenantUsersController,
          :index
    end
  end

  if Application.compile_env(:admin, :dev_routes) do
    scope "/dev" do
      pipe_through :browser

      live_dashboard "/dashboard", metrics: AdminWeb.Telemetry
      forward "/mailbox", Plug.Swoosh.MailboxPreview
    end
  end

  scope "/", AdminWeb do
    live_session :require_authenticated_user, on_mount: [] do
      live "/dashboard", DashboardLive, :index
    end

    post "/users/log-in", UserSessionController, :create
    get "/missing", MissingController, :show
    # A later scope repeating a route Phoenix already matched: noted, left out.
    get "/missing", MissingController, :show
    get @dynamic_path, PageController, :home
    match :*, "/any", PageController, :home

    # alias: false drops the enclosing AdminWeb alias.
    scope "/plain", alias: false do
      get "/health", HealthController, :show
    end

    resources "/users", UserController, only: [:index, :show]
  end
end
