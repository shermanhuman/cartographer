defmodule Rosearch.ApiClient do
  @moduledoc """
  A Req-based HTTP client to communicate with the admin API.
  """

  def new(opts \\ []) do
    base_url = Application.get_env(:rosearch, :admin_api_url, "http://localhost:4000/api/v1")

    Req.new(base_url: base_url)
    |> Req.Request.put_header("accept", "application/json")
    |> put_token(Keyword.get(opts, :token))
  end

  def fetch(path, opts \\ []) do
    path |> get(opts) |> unwrap()
  end

  def submit(path, body, opts \\ []) do
    path |> post(body, opts) |> unwrap()
  end

  def unwrap({:ok, %Req.Response{status: status, body: %{"data" => data}}})
      when status in 200..299,
      do: {:ok, data}

  def unwrap(_result), do: {:error, "Something went wrong. Please try again."}

  def get(path, opts \\ []), do: request(:get, path, [], opts)

  def post(path, body, opts \\ []), do: request(:post, path, [json: body], opts)

  def delete(path, opts \\ []), do: request(:delete, path, [], opts)

  defp request(method, path, extra, opts) do
    req = new(opts)
    call_opts = [method: method, url: path] ++ extra ++ Keyword.get(opts, :req, [])
    call_opts = Keyword.put(call_opts, :receive_timeout, 20_000)

    task = Task.async(fn -> run(req, call_opts) end)
    Task.await(task)
  end

  defp run(req, call_opts) do
    Req.request(req, call_opts)
  rescue
    exception -> {:error, exception}
  end

  defp put_token(req, nil), do: req
  defp put_token(req, token), do: Req.Request.put_header(req, "authorization", "Bearer #{token}")
end
