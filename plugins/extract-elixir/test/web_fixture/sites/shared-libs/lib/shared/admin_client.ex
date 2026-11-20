defmodule Shared.AdminClient do
  @moduledoc """
  Req client for the one admin endpoint the marketing sites use:
  `POST {base_url}/leads`.
  """

  require Logger

  @max_retries 2

  def configured?, do: base_url() != nil and service_key() != nil

  def create_lead(lead, source, client_ip, opts \\ []) when is_map(lead) and is_binary(source) do
    if configured?() do
      [url: "/leads", json: %{"lead" => lead, "source" => source}]
      |> new(client_ip, opts[:idempotency_key])
      |> attempt(0)
    else
      Logger.error("AdminClient is not configured")
      {:error, :unavailable}
    end
  end

  defp new(opts, client_ip, idempotency_key) do
    [base_url: base_url(), receive_timeout: 5_000, retry: false]
    |> Keyword.merge(opts)
    |> Req.new()
    |> Req.Request.put_header("x-service-key", service_key())
    |> put_optional_header("idempotency-key", idempotency_key)
    |> put_optional_header("x-forwarded-for", client_ip)
  end

  defp attempt(req, retries) do
    case classify(post(req)) do
      {:retry, _error} when retries < @max_retries -> attempt(req, retries + 1)
      {:retry, error} -> {:error, error}
      result -> result
    end
  end

  defp post(req) do
    Req.post(req)
  rescue
    exception -> {:error, exception}
  end

  defp classify({:ok, %Req.Response{status: 201, body: %{"data" => data}}}), do: {:ok, data}
  defp classify({:ok, %Req.Response{status: 409}}), do: {:error, :already_registered}
  defp classify(_other), do: {:retry, :unavailable}

  defp put_optional_header(req, _name, nil), do: req
  defp put_optional_header(req, name, value), do: Req.Request.put_header(req, name, value)

  defp base_url, do: config()[:base_url]
  defp service_key, do: config()[:service_key]
  defp config, do: Application.get_env(:shared, :admin_client, [])
end
