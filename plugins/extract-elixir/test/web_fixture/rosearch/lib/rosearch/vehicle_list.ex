defmodule Rosearch.VehicleList do
  alias Rosearch.ApiClient

  def preferences(token) do
    ApiClient.get("/auth/me/preferences", token: token)
  end

  def cleanup(slug, page, token) do
    ApiClient.get("/tenants/#{slug}/vehicle-cleanup?page=#{page}", token: token)
  end

  def wont_fix(slug, vehicle_id, token) do
    "/tenants/#{slug}/vehicle-cleanup/wont-fix"
    |> ApiClient.post(%{vehicle_id: vehicle_id}, token: token)
    |> ApiClient.unwrap()
  end

  def boilerplate(slug, token) do
    ApiClient.get(path(slug), token: token)
  end

  def makes(token), do: ApiClient.fetch("/vehicles/supported-makes", token: token)

  def remove_user(slug, id, token) do
    ApiClient.delete("/tenants/#{slug}/users/#{id}", token: token)
  end

  def year_ranges(query, token) do
    ApiClient.fetch("/vehicles/year-ranges?" <> query, token: token)
  end

  defp path(slug), do: "/tenants/#{slug}/boilerplate"
end
