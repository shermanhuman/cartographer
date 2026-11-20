defmodule AdminWeb.InternalRouter do
  use Phoenix.Router

  scope "/api/v1/internal", AdminWeb.API.V1 do
    post "/invites", BetaInviteController, :create
  end

  # Already served by AdminWeb.Router.
  post "/api/v1/leads", AdminWeb.API.V1.LeadController, :create
end
