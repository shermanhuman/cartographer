defmodule Shared.Turnstile do
  @verify_url "https://challenges.cloudflare.com/turnstile/v0/siteverify"

  def verify(token, remote_ip) do
    request =
      Req.new(url: @verify_url, form: [response: token, remoteip: remote_ip], retry: false)
      |> Req.merge(Application.get_env(:shared, :turnstile_req_opts, []))

    case Req.post(request) do
      {:ok, %Req.Response{status: 200, body: %{"success" => true}}} -> :ok
      _other -> {:error, :invalid}
    end
  end
end
