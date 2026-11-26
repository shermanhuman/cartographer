# Regression corpus: the probe shapes from cartographer#35's review rounds
# (h1-h7), as {"probe/shape", %{path => source}}. Generated once by
# scratch extract_shapes.exs; edit by hand to add shapes.
[
  {"http/write-1",
   %{
     "lib/a.ex" => "defmodule Direct do\n  @api \"https://api.stripe.com/v1\"\n  def a, do: Req.get!(base_url: @api, url: \"/charges\")\n  def b, do: Req.post(Req.new(base_url: \"https://api.stripe.com/v1\"), url: \"/refunds\")\nend\ndefmodule Interp do\n  def get(ns), do: fetch(\"/check\", ns)\n  defp fetch(path, ns), do: Req.get(Req.new(base_url: \"http://admin.\#{ns}.svc:4000\"), url: path)\nend\ndefmodule Interp2 do\n  def get(region), do: fetch(\"/v1/x\", region)\n  defp fetch(path, region), do: Req.get(Req.new(base_url: \"https://api.\#{region}.example.com/base\"), url: path)\nend\ndefmodule Mixed do\n  def charge, do: stripe(\"/v1/charges\")\n  def check, do: admin(\"/api/check\")\n  defp stripe(path), do: Req.post(Req.new(base_url: \"https://api.stripe.com\"), url: path)\n  defp admin(path), do: Req.get(Req.new(base_url: \"http://admin.breakdown.svc:4000\"), url: path)\nend\ndefmodule VarNoise do\n  def go, do: fetch(\"/api/check\")\n  defp fetch(path) do\n    base = Application.fetch_env!(:app, :admin_url)\n    Req.get(Req.new(base_url: base, headers: [{\"referer\", \"https://docs.example.com/help\"}]), url: path)\n  end\nend\ndefmodule Loop do\n  def go, do: fetch(\"/api/check\")\n  defp fetch(path), do: Req.get(Req.new(base_url: System.get_env(\"ADMIN_URL\", \"http://localhost:4000\")), url: path)\nend\ndefmodule Upper do\n  def go, do: fetch(\"/x\")\n  defp fetch(path), do: Req.get(Req.new(base_url: \"HTTPS://API.Stripe.COM:443/v1/\"), url: path)\nend\ndefmodule V6 do\n  def go, do: fetch(\"/x\")\n  defp fetch(path), do: Req.get(Req.new(base_url: \"http://[2001:db8::1]:8080/v1\"), url: path)\nend\ndefmodule OtherFn do\n  def go, do: fetch(\"/x\")\n  defp fetch(path), do: Req.get(Req.new(base_url: endpoint()), url: path)\n  defp endpoint, do: Application.get_env(:app, :endpoint) || raise \"set https://example.com/docs in config\"\nend\n"
   }},
  {"run2/remote-project-macro",
   %{
     "lib/c.ex" => "defmodule App.Caller do\n  def go(x), do: App.Thing.touch(x)\nend\n",
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      use Ecto.Schema\n      def touch(x), do: x\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\nend\n"
   }},
  {"run2/loopback",
   %{
     "lib/a.ex" => "defmodule Loop do\n  def go, do: fetch(\"/api/check\")\n  defp fetch(path), do: Req.get(Req.new(base_url: \"http://localhost:4000/api\"), url: path)\nend\n"
   }},
  {"run/dyn",
   %{
     "lib/settings.ex" => "defmodule App.Settings do\n  use Ecto.Schema\n  for n <- [:a, :b] do\n    def unquote(n)(), do: 1\n  end\n  def both, do: {a(), b()}\nend\n",
     "lib/user.ex" => "defmodule App.User do\n  import Ecto.Query\n  import App.Settings\n  def x, do: a()\nend\n"
   }},
  {"run/gettext",
   %{
     "lib/cc.ex" => "defmodule AppWeb.CoreComponents do\n  use Phoenix.Component\n  import AppWeb.Gettext\n  def t(x), do: gettext(\"hello\") <> x\nend\n",
     "lib/gt.ex" => "defmodule AppWeb.Gettext do\n  use Gettext, otp_app: :app\nend\n"
   }},
  {"run/only",
   %{
     "lib/a.ex" => "defmodule A do\n  import Ecto.Query, only: [from: 2]\n  def q(x), do: {from(y in x, select: y), cast(x, %{}, [])}\nend\n"
   }},
  {"run/quote",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      import Ecto.Changeset\n      def touch(x), do: x\n    end\n  end\nend\ndefmodule App.Thing do\n  use App.Schema\n  def go(x), do: touch(x)\nend\n"
   }},
  {"run/heex",
   %{
     "lib/live/page_live.ex" => "defmodule AppWeb.PageLive do\n  use AppWeb, :live_view\n  def mount(_p, _s, socket), do: {:ok, assign(socket, :a, 1)}\n  def inline(assigns) do\n    ~H\"<.link navigate=\\\"/x\\\">x</.link>\"\n  end\nend\n",
     "lib/live/page_live.html.heex" => "<.link navigate=\"/x\">x</.link>\n<%= live_title_tag(\"t\") %>\n",
     "lib/web.ex" => "defmodule AppWeb do\n  def live_view do\n    quote do\n      use Phoenix.LiveView\n      import Phoenix.HTML\n    end\n  end\n  defmacro __using__(which), do: apply(__MODULE__, which, [])\nend\n"
   }},
  {"run/heex2",
   %{
     "lib/live/c.ex" => "defmodule AppWeb.C do\n  use Phoenix.Component\n  def inline(assigns) do\n    ~H\"<.link navigate=\\\"/x\\\">x</.link>\"\n  end\nend\n",
     "lib/live/c.html.heex" => "<.link navigate=\"/x\">x</.link>\n"
   }},
  {"run/capture",
   %{
     "lib/repo.ex" => "defmodule App.Repo do\n  use Ecto.Repo, otp_app: :app\nend\n",
     "lib/w.ex" => "defmodule App.W do\n  def go(xs), do: {Enum.map(xs, &App.Repo.insert/1), App.Repo.insert(hd(xs)), apply(App.Repo, :get, [1, 2])}\nend\n"
   }},
  {"http2/write-1",
   %{
     "lib/a.ex" => "defmodule CfgFallback do\n  @stripe \"https://api.stripe.com/v1\"\n  def charge, do: stripe(\"/charges\")\n  def check, do: admin(\"/api/check\")\n  defp stripe(path), do: Req.post(Req.new(base_url: @stripe), url: path)\n  defp admin(path), do: Req.get(Req.new(base_url: Application.fetch_env!(:app, :admin_url)), url: path)\nend\ndefmodule InterpFallback do\n  def charge, do: stripe(\"/charges\")\n  def check(ns), do: admin(\"/api/check\", ns)\n  defp stripe(path), do: Req.post(Req.new(base_url: \"https://api.stripe.com/v1\"), url: path)\n  defp admin(path, ns), do: Req.get(Req.new(base_url: \"http://admin.\#{ns}.svc:4000\"), url: path)\nend\ndefmodule ParamFallback do\n  def charge, do: request(\"https://api.stripe.com\", \"/v1/charges\")\n  def check, do: request(admin(), \"/api/check\")\n  defp admin, do: Application.fetch_env!(:app, :admin_url)\n  defp request(base, path), do: Req.get(base_url: base, url: path)\nend\ndefmodule HelperShape do\n  @api \"https://api.stripe.com/v1\"\n  def charge, do: get(\"/charges\")\n  defp get(path), do: Req.get!(client(), url: path)\n  defp client, do: Req.new(base_url: @api)\nend\ndefmodule HelperMixed do\n  @stripe \"https://api.stripe.com/v1\"\n  def charge, do: stripe_get(\"/charges\")\n  def check, do: admin_get(\"/api/check\")\n  defp stripe_get(path), do: Req.get!(stripe_client(), url: path)\n  defp admin_get(path), do: Req.get!(admin_client(), url: path)\n  defp stripe_client, do: Req.new(base_url: @stripe)\n  defp admin_client, do: Req.new(base_url: Application.fetch_env!(:app, :admin_url))\nend\n"
   }},
  {"quote/defaults",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      import Ecto.Changeset\n      def touch(x, opts \\\\ []), do: {x, opts}\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  def go(x), do: touch(x)\nend\n"
   }},
  {"quote/dispatch",
   %{
     "lib/page.ex" => "defmodule AppWeb.PageLive do\n  use AppWeb, :live_view\n  def mount(_p, _s, socket), do: ok(socket)\nend\n",
     "lib/web.ex" => "defmodule AppWeb do\n  def live_view do\n    quote do\n      use Phoenix.LiveView\n      def ok(socket), do: {:ok, socket}\n    end\n  end\n  defmacro __using__(which) when is_atom(which), do: apply(__MODULE__, which, [])\nend\n"
   }},
  {"quote/unquote-helper",
   %{
     "lib/c.ex" => "defmodule App.Caller do\n  def go(x), do: App.Thing.touch(x)\nend\n",
     "lib/s.ex" => "defmodule App.Schema do\n  def helpers do\n    quote do\n      def touch(x), do: x\n    end\n  end\n  defmacro __using__(_) do\n    quote do\n      use Ecto.Schema\n      import Ecto.Changeset\n      unquote(helpers())\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  def go(x), do: touch(x)\nend\n"
   }},
  {"quote/nested",
   %{
     "lib/a.ex" => "defmodule App.A do\n  defmacro __using__(_) do\n    quote do\n      import Ecto.Changeset\n      use App.B\n    end\n  end\nend\n",
     "lib/b.ex" => "defmodule App.B do\n  defmacro __using__(_) do\n    quote do\n      def b_fun(x), do: x\n      def unquote(:\"gen_\#{1}\")(), do: 1\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.A\n  def go(x), do: {b_fun(x), gen_1()}\nend\n"
   }},
  {"quote/delegate",
   %{
     "lib/c.ex" => "defmodule App.Caller do\n  def go(x), do: App.Thing.fmt(x)\nend\n",
     "lib/f.ex" => "defmodule App.Fmt do\n  def fmt(x), do: x\nend\n",
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      import Ecto.Changeset\n      defdelegate fmt(x), to: App.Fmt\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  def go(x), do: fmt(x)\nend\n"
   }},
  {"quote/remote-literal",
   %{
     "lib/c.ex" => "defmodule App.Caller do\n  def go(x), do: {App.Thing.touch(x), App.Thing.__schema__(:fields)}\nend\n",
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      use Ecto.Schema\n      def touch(x), do: x\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  def go(x), do: {touch(x), __schema__(:fields), changeset(x)}\nend\n"
   }},
  {"http3/write-1",
   %{
     "lib/a.ex" => "defmodule CaseMixed do\n  def charge, do: Req.get!(client(:stripe), url: \"/charges\")\n  def check, do: Req.get!(client(:admin), url: \"/api/check\")\n  defp client(kind) do\n    base =\n      case kind do\n        :stripe -> \"https://api.stripe.com/v1\"\n        :admin -> Application.fetch_env!(:app, :admin_url)\n      end\n    Req.new(base_url: base)\n  end\nend\ndefmodule ConcatUrl do\n  @api \"https://api.stripe.com/v1\"\n  def charge, do: Req.post!(Req.new(base_url: @api), url: \"/charges\")\n  def check(host), do: Req.get!(host <> \"/api/check\")\n  def check2, do: Req.get!(url: Application.fetch_env!(:app, :admin) <> \"/api/check2\")\n  def check3(opts), do: Req.get!(Req.new(opts), url: \"/api/check3\")\nend\ndefmodule GetEnvDefault do\n  def charge, do: Req.get!(stripe(), url: \"/charges\")\n  def check, do: Req.get!(admin(), url: \"/api/check\")\n  defp stripe, do: Req.new(base_url: \"https://api.stripe.com/v1\")\n  defp admin, do: Req.new(base_url: System.get_env(\"ADMIN_URL\", \"http://admin.default.svc:4000\"))\nend\ndefmodule Loopback do\n  def charge, do: Req.get!(stripe(), url: \"/charges\")\n  def local, do: Req.get!(dev(), url: \"/x\")\n  defp stripe, do: Req.new(base_url: \"https://API.Stripe.com/v1\")\n  defp dev, do: Req.new(base_url: \"http://localhost:4000\")\nend\ndefmodule NoValues do\n  def check, do: Req.get!(url: \"https://API.GitHub.com/repos\")\nend\n"
   }},
  {"http4/write-1",
   %{
     "lib/a.ex" => "defmodule Upper do\n  def a, do: Req.get!(\"HTTPS://API.GitHub.COM:443/repos\")\n  def a2, do: Req.get!(\"https://API.GitHub.com/repos\")\n  def a3, do: Req.get!(\"https://api.github.com/repos\")\n  def b, do: Req.get!(Req.new(base_url: \"https://API.Stripe.COM/v1\"), url: \"/charges\")\n  def c, do: Req.get!(client(), url: \"/x\")\n  defp client, do: Req.new(base_url: \"https://Api.Stripe.com/v1\")\nend\n"
   }},
  {"quote3/cond",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(opts) do\n    quote do\n      if unquote(Keyword.get(opts, :touch, true)) do\n        def touch(x), do: x\n      end\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  import Ecto.Changeset\n  def go(x), do: touch(x)\nend\n"
   }},
  {"quote3/for",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote unquote: false do\n      for n <- [:touch] do\n        def unquote(n)(x), do: x\n      end\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  import Ecto.Changeset\n  def go(x), do: touch(x)\nend\n"
   }},
  {"quote3/deep",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_), do: quote(do: use App.L1)\nend\ndefmodule App.L1 do\n  defmacro __using__(_), do: quote(do: use App.L2)\nend\ndefmodule App.L2 do\n  defmacro __using__(_), do: quote(do: use App.L3)\nend\ndefmodule App.L3 do\n  defmacro __using__(_) do\n    quote do\n      def touch(x), do: x\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  import Ecto.Changeset\n  def go(x), do: touch(x)\nend\n"
   }},
  {"quote3/deep2",
   %{
     "lib/t.ex" => "defmodule App.PageLive do\n  use AppWeb, :live_view\n  import Ecto.Changeset\n  def mount(_, _, s), do: touch(s)\nend\n",
     "lib/w.ex" => "defmodule AppWeb do\n  def live_view do\n    quote do\n      use Phoenix.LiveView\n      unquote(html_helpers())\n    end\n  end\n  defp html_helpers do\n    quote do\n      use App.Helpers\n    end\n  end\n  defmacro __using__(which) when is_atom(which), do: apply(__MODULE__, which, [])\nend\ndefmodule App.Helpers do\n  defmacro __using__(_), do: quote(do: use App.Inner)\nend\ndefmodule App.Inner do\n  defmacro __using__(_) do\n    quote do\n      def touch(x), do: x\n    end\n  end\nend\n"
   }},
  {"quote3/misc",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      def touch(x) when is_map(x), do: x\n      def touch(x), do: x\n      defdelegate fmt(x), to: App.Format, as: :format\n      defp secret(x), do: x\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  import Ecto.Changeset\n  def go(x), do: {touch(x), fmt(x), format(x), secret(x)}\nend\ndefmodule App.Other do\n  use App.Schema\n  def go(x), do: {touch(x), App.Thing.fmt(x)}\nend\ndefmodule App.NoUse do\n  import Ecto.Changeset\n  def go(x), do: {secret(x), touch(x)}\nend\n"
   }},
  {"quote3/order",
   %{
     "lib/a.ex" => "defmodule App.Thing do\n  use App.Schema\n  import Ecto.Changeset\n  def go(x), do: touch(x)\nend\n",
     "lib/z.ex" => "defmodule App.Schema do\n  defmacro __using__(_), do: quote(do: def(touch(x), do: x))\nend\n"
   }},
  {"quote3/inner",
   %{
     "lib/s.ex" => "defmodule App.Schema do\n  defmacro __using__(_) do\n    quote do\n      defmodule Inner do\n        def touch(x), do: x\n      end\n    end\n  end\nend\n",
     "lib/t.ex" => "defmodule App.Thing do\n  use App.Schema\n  import Ecto.Changeset\n  def go(x), do: touch(x)\nend\n"
   }},
  {"http5/write-1",
   %{
     "lib/a.ex" => "defmodule TupleKey do\n  # decision 17's tuple shape, with the key from config in the same tuple\n  def charge, do: Req.get!(client(:stripe), url: \"/charges\")\n  defp client(kind) do\n    {base, key} =\n      case kind do\n        :stripe -> {\"https://api.stripe.com/v1\", Application.fetch_env!(:app, :stripe_key)}\n      end\n    Req.new(base_url: base, auth: {:bearer, key})\n  end\nend\ndefmodule TupleLit do\n  def charge, do: Req.get!(client(:stripe), url: \"/charges\")\n  defp client(kind) do\n    {base, key} = case kind do :stripe -> {\"https://api.stripe.com/v1\", \"k\"} end\n    Req.new(base_url: base, auth: {:bearer, key})\n  end\nend\ndefmodule ParamBranch do\n  # one branch is a parameter, not a call\n  def charge, do: Req.get!(client(:stripe, nil), url: \"/charges\")\n  def check(u), do: Req.get!(client(:admin, u), url: \"/api/check\")\n  defp client(kind, admin_url) do\n    base =\n      case kind do\n        :stripe -> \"https://api.stripe.com/v1\"\n        :admin -> admin_url\n      end\n    Req.new(base_url: base)\n  end\nend\ndefmodule VarBranch do\n  # the config read is bound to another variable first\n  def check, do: Req.get!(client(:admin), url: \"/api/check\")\n  defp client(kind) do\n    cfg = Application.fetch_env!(:app, :admin_url)\n    base = if kind == :stripe, do: \"https://api.stripe.com/v1\", else: cfg\n    Req.new(base_url: base)\n  end\nend\ndefmodule Concat do\n  def charge do\n    base = \"https://api.stripe.com\" <> \"/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/charges\")\n  end\nend\ndefmodule OrDefault do\n  def a(opts) do\n    base = Keyword.get(opts, :base_url) || \"https://api.anthropic.com\"\n    Req.post!(Req.new(base_url: base), url: \"/v1/messages\")\n  end\n  def b(opts), do: Req.post!(Req.new(base_url: base_url(opts)), url: \"/v1/messages\")\n  defp base_url(opts), do: Keyword.get(opts, :base_url) || \"https://api.anthropic.com\"\nend\ndefmodule EnvCase do\n  def charge do\n    base = case Mix.env() do :prod -> \"https://api.stripe.com/v1\"; _ -> \"https://api.stripe.com/v1\" end\n    Req.get!(Req.new(base_url: base), url: \"/charges\")\n  end\nend\ndefmodule Sigil do\n  def charge do\n    base = ~s(https://api.stripe.com/v1)\n    Req.get!(Req.new(base_url: base), url: \"/charges\")\n  end\nend\ndefmodule FinanceShape do\n  defp get(provider, path) do\n    {base, key} =\n      case provider do\n        \"stripe\" -> {\"https://api.stripe.com/v1\", Application.get_env(:app, :k)[:secret_key]}\n        \"mercury\" -> {\"https://api.mercury.com/api/v1\", nil}\n      end\n    Req.get(url: base <> path, auth: {:bearer, key})\n  end\n  def list, do: get(\"stripe\", \"/charges\")\nend\n"
   }},
  {"http6/write-1",
   %{
     "lib/a.ex" => "defmodule VarCase do\n  def charge, do: Req.get!(client(:stripe), url: \"/charges\")\n  def check, do: Req.get!(client(:admin), url: \"/api/check\")\n  defp client(kind) do\n    cfg = Application.fetch_env!(:app, :admin_url)\n    base =\n      case kind do\n        :stripe -> \"https://api.stripe.com/v1\"\n        :admin -> cfg\n      end\n    Req.new(base_url: base)\n  end\nend\n"
   }},
  {"http7/write-1",
   %{
     "lib/a.ex" => "defmodule S1FnParam do\n  # the request's `base` is the fn parameter; the literal `base =` is another binding\n  def ping(mirrors) do\n    base = \"https://api.stripe.com/v1\"\n    Enum.map(mirrors, fn base -> Req.get!(Req.new(base_url: base), url: \"/health\") end)\n  end\nend\ndefmodule S2CasePattern do\n  def ping(opts) do\n    base = \"https://api.stripe.com/v1\"\n    case Keyword.fetch(opts, :base_url) do\n      {:ok, base} -> Req.get!(Req.new(base_url: base), url: \"/health\")\n      :error -> :none\n    end\n  end\nend\ndefmodule S3With do\n  def ping(opts) do\n    base = \"https://api.stripe.com/v1\"\n    with {:ok, base} <- Keyword.fetch(opts, :base_url), do: Req.get!(Req.new(base_url: base), url: \"/health\")\n  end\nend\ndefmodule S4For do\n  def ping do\n    base = \"https://api.stripe.com/v1\"\n    for base <- Application.fetch_env!(:app, :mirrors), do: Req.get!(Req.new(base_url: base), url: \"/health\")\n  end\nend\ndefmodule S5InnerFnBinding do\n  # the only `=` is inside an unrelated fn; the request's `base` is the parameter\n  def ping(base) do\n    _f = fn -> base = \"https://api.stripe.com/v1\"; base end\n    Req.get!(Req.new(base_url: base), url: \"/health\")\n  end\nend\ndefmodule T1TwoHosts do\n  def stripe, do: get(:stripe, \"/charges\")\n  def mercury, do: get(:mercury, \"/accounts\")\n  defp get(kind, path) do\n    base = case kind do\n      :stripe -> \"https://api.stripe.com/v1\"\n      :mercury -> \"https://api.mercury.com/api/v1\"\n    end\n    Req.get!(Req.new(base_url: base), url: path)\n  end\nend\ndefmodule T2IfNoElse do\n  def ping(prod?) do\n    base = if prod?, do: \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T3Unless do\n  def ping(t?) do\n    base = unless t?, do: \"https://api.stripe.com/v1\", else: \"http://localhost:12111\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T4CondVar do\n  def ping(x) do\n    base = cond do\n      x == 1 -> \"https://api.stripe.com/v1\"\n      true -> x\n    end\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T5Block do\n  def ping(x) do\n    base = (\n      _ = IO.inspect(x)\n      \"https://api.stripe.com/v1\"\n    )\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T6IfBlock do\n  def ping(x) do\n    base =\n      if x do\n        IO.puts(\"a\")\n        \"https://api.stripe.com/v1\"\n      else\n        \"https://api.stripe.com/v1\"\n      end\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T7Attr do\n  @api \"https://api.stripe.com/v1\"\n  def ping(x) do\n    base = x || @api\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\n  def lit do\n    base = @api\n    Req.get!(Req.new(base_url: base), url: \"/y\")\n  end\nend\ndefmodule T8Nested do\n  def ping do\n    {{:ok, base}, _} = {{:ok, \"https://api.stripe.com/v1\"}, 1}\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T9ThreeTuple do\n  def ping do\n    {_, base, _} = {1, \"https://api.stripe.com/v1\", 2}\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\n  def wrong_len do\n    {_, base} = {1, \"https://api.stripe.com/v1\", 2}\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T10Rebind do\n  def ping(sandbox?) do\n    base = \"https://api.stripe.com/v1\"\n    base = if sandbox?, do: \"https://api.stripe.com/test\", else: base\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T11List do\n  @hosts [\"https://api.stripe.com/v1\", \"https://api.mercury.com/api/v1\"]\n  def ping do\n    base = @hosts\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T12Pipe do\n  def ping(k) do\n    base = k |> case do\n      :a -> \"https://api.stripe.com/v1\"\n    end\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule T13EmptyBlock do\n  def ping do\n    base = (); Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\n"
   }},
  {"http8/write-1",
   %{
     "lib/a.ex" => "defmodule V1ImplicitTry do\n  def ping do\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  rescue\n    e -> {:error, e}\n  end\nend\ndefmodule V2ExplicitTry do\n  def ping do\n    try do\n      base = \"https://api.stripe.com/v1\"\n      Req.get!(Req.new(base_url: base), url: \"/x\")\n    rescue\n      e -> {:error, e}\n    end\n  end\nend\ndefmodule V3NestedUse do\n  def ping(x) do\n    base = \"https://api.stripe.com/v1\"\n    if x, do: Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule V4ParamThenRebind do\n  def ping(base) do\n    Req.get!(Req.new(base_url: base), url: \"/a\")\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/b\")\n  end\nend\ndefmodule V5SeqTwoHosts do\n  def ping do\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/a\")\n    base = \"https://api.mercury.com/api/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/b\")\n  end\nend\ndefmodule V6CondUse do\n  def ping(x) do\n    base = \"https://api.stripe.com/v1\"\n    cond do\n      x == base -> :same\n      true -> Req.get!(Req.new(base_url: base), url: \"/x\")\n    end\n  end\nend\ndefmodule V7Pin do\n  def ping(x) do\n    base = \"https://api.stripe.com/v1\"\n    case x do\n      ^base -> :same\n      _ -> Req.get!(Req.new(base_url: base), url: \"/x\")\n    end\n  end\nend\ndefmodule V8OneLineBlock do\n  def ping, do: (base = \"https://api.stripe.com/v1\"; Req.get!(Req.new(base_url: base), url: \"/x\"))\nend\ndefmodule V9MultiClause do\n  def ping(:lit) do\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\n  def ping(base) do\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule V10OtherWith do\n  def ping(opts) do\n    base = \"https://api.stripe.com/v1\"\n    with {:ok, key} <- Keyword.fetch(opts, :key), do: Req.get!(Req.new(base_url: base, auth: {:bearer, key}), url: \"/x\")\n  end\nend\ndefmodule V11Default do\n  def ping(base \\\\ \"https://api.stripe.com/v1\") do\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule V12Guard do\n  def ping(x) when is_binary(x) do\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule V13Bodyless do\n  def ping(x \\\\ 1)\n  def ping(_x) do\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule V14Delegate do\n  defdelegate other(x), to: V12Guard, as: :ping\n  def ping do\n    base = \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\n"
   }},
  {"http9/write-1",
   %{
     "lib/a.ex" => "defmodule W1GetEnvDefault do\n  def ping do\n    Req.get!(Req.new(base_url: Application.get_env(:app, :api, \"https://api.stripe.com/v1\")), url: \"/x\")\n  end\nend\ndefmodule W2OrDefault do\n  def ping do\n    Req.get!(Req.new(base_url: System.get_env(\"API_URL\") || \"https://api.stripe.com/v1\"), url: \"/x\")\n  end\nend\ndefmodule W3VarOrDefault do\n  def ping(opts) do\n    base = opts[:base] || \"https://api.stripe.com/v1\"\n    Req.get!(Req.new(base_url: base), url: \"/x\")\n  end\nend\ndefmodule W4Config do\n  def ping do\n    Req.get!(Req.new(base_url: Application.fetch_env!(:app, :api)), url: \"/x\")\n  end\nend\n"
   }}
]
