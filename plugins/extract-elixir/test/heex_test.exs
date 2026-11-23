# Run: mise exec -- elixir plugins/extract-elixir/test/heex_test.exs
Code.require_file("../heex_tokenizer.exs", __DIR__)
Code.require_file("../heex.exs", __DIR__)
Code.require_file("../source.exs", __DIR__)
Code.require_file("../http.exs", __DIR__)
Code.require_file("../facts.exs", __DIR__)
ExUnit.start()

defmodule Cartographer.HEExTest do
  use ExUnit.Case, async: true

  alias Cartographer.ElixirFacts

  # Two Phoenix apps in one repo (as breakdown-sites has), each with its own
  # CoreComponents imported through `use <App>Web, :kind`.
  @app ~S'''
  defmodule NAMEWeb do
    def live_view do
      quote do
        use Phoenix.LiveView
        unquote(html_helpers())
      end
    end

    def live_component do
      quote do
        use Phoenix.LiveComponent
        unquote(html_helpers())
      end
    end

    def html do
      quote do
        use Phoenix.Component
        unquote(html_helpers())
      end
    end

    defp html_helpers do
      quote do
        import NAMEWeb.CoreComponents
        alias Phoenix.LiveView.JS
      end
    end

    defmacro __using__(which) when is_atom(which), do: apply(__MODULE__, which, [])
  end
  '''

  @files %{
    "lib/shop_web.ex" => String.replace(@app, "NAME", "Shop"),
    "lib/other_web.ex" => String.replace(@app, "NAME", "Other"),
    "lib/shop_web/core_components.ex" => ~S'''
    defmodule ShopWeb.CoreComponents do
      use Phoenix.Component
      alias Phoenix.LiveView.JS

      def button(assigns), do: ~H"<button phx-click={@click}>{render_slot(@inner_block)}</button>"
      def icon(assigns), do: ~H"<span />"
      def unfilled(assigns), do: ~H"<i phx-submit={@on_submit} />"

      def flash(assigns) do
        ~H"""
        <div phx-click={JS.push("lv:clear-flash")}></div>
        """
      end
    end
    ''',
    "lib/other_web/core_components.ex" => ~S'''
    defmodule OtherWeb.CoreComponents do
      use Phoenix.Component
      def icon(assigns), do: ~H"<b />"
    end
    ''',
    "lib/shop_web/live/page_live.ex" => ~S'''
    defmodule ShopWeb.PageLive do
      use ShopWeb, :live_view
      @events ~w(a1 a2)
      @more ["", "b1"]

      def render(assigns) do
        ~H"""
        <div phx-click="save" phx-hook="Pad" id="p">
          <.button click="delete">Del</.button>
          <.icon />
          <.live_component module={ShopWeb.RowComponent} id="r" />
          <span phx-click={if @on, do: "open", else: "close"} phx-change={@dyn_name}></span>
          <a phx-click={if @x == "payouts", do: "go"}></a>
          <div phx-click={"filter_#{@n}"} phx-blur={JS.push("blurred") |> JS.hide()}></div>
          <p>{format(@n)}</p>
          <button phx-click={show_modal("confirm")}>?</button>
          <.button click={@event}>Any</.button>
          <b phx-click={if @x, do: "save", else: ""}></b>
        </div>
        """
      end

      defp format(n), do: n
      defp show_modal(id), do: id

      def handle_event("save", _, s), do: {:noreply, s}
      def handle_event("delete", _, s), do: {:noreply, s}
      def handle_event(e, _, s) when e in ~w(open close), do: {:noreply, s}
      def handle_event(e, _, s) when e in @events, do: {:noreply, s}
      def handle_event("go", _, s), do: {:noreply, s}
      def handle_event("blurred", _, s), do: {:noreply, s}
      def handle_event("dead", _, s), do: {:noreply, s}
      def handle_event("", _, s), do: {:noreply, s}
      def handle_event(e, _, s) when e in @more, do: {:noreply, s}
    end
    ''',
    "lib/shop_web/live/row_component.ex" => ~S'''
    defmodule ShopWeb.RowComponent do
      use ShopWeb, :live_component

      def render(assigns) do
        ~H"""
        <div><b phx-click="toggle" phx-target={@myself}></b><i phx-click="save"></i>
          <u phx-click={JS.push("pick", target: @myself, zz_never_an_atom_q7: true)}></u></div>
        """
      end

      def handle_event("toggle", _, s), do: {:noreply, s}
      def handle_event("pick", _, s), do: {:noreply, s}
    end
    ''',
    "lib/shop_web/report.ex" => ~S'''
    defmodule ShopWeb.Report do
      def handle_event("export", _, s), do: {:noreply, s}
    end
    ''',
    "lib/shop_web/live/revenue_live.ex" => ~S'''
    defmodule ShopWeb.RevenueLive do
      use ShopWeb, :live_view
      def render(assigns), do: ~H|<button phx-click="export">csv</button>|
      def handle_event(event, params, socket), do: ShopWeb.Report.handle_event(event, params, socket)
    end
    ''',
    "lib/other_web/live/x_live.ex" => ~S'''
    defmodule OtherWeb.XLive do
      use OtherWeb, :live_view
      def render(assigns), do: ~H"<.icon />"
    end
    ''',
    "lib/shop_web/controllers/page_html.ex" => ~S'''
    defmodule ShopWeb.PageHTML do
      use ShopWeb, :html
      embed_templates "page_html/*"
    end
    ''',
    "lib/shop_web/controllers/page_html/home.html.heex" => ~S'''
    <.button click="from_home">Hi</.button>
    ''',
    "lib/shop_web/live/founders.ex" => ~S'''
    defmodule ShopWeb.FoundersLive do
      use ShopWeb, :live_view
      def handle_event("signed", _, s), do: {:noreply, s}
    end
    ''',
    "lib/shop_web/live/founders.html.heex" => ~S'''
    <div id="sig" phx-hook=".Sig">
      <script :type={Phoenix.LiveView.ColocatedHook} name=".Sig">
        // this.pushEvent("ghost", {});
        const note = "this.pushEvent(\"inside\")";
        const label = `${this.pushEvent("templated", {})}`;
        const r = /"/; this.pushEvent("\u0073igned", {});
        this.pushEvent("row_" + id, {});
        x++ / this.pushEvent("divided") / 2; obj.return / this.pushEvent("divided") / 2;
        if (ok) /"/.test(s); this.pushEvent("divided");
        const smile = "\uD83D\uDE00"; const lone = "\uD83D";
        const p = String.raw`C:\users`; const q = "\xZZ"; const one = 1./2; this.pushEvent("after_raw");
        export default { mounted() { this.pushEvent("signed", {}); this.pushEventTo("#x", "aimed", {}); this.pushEvent(this.el.dataset.event, {}) } }
      </script>
    </div>
    ''',
    "lib/shop_web/mail_html.ex" => ~S'''
    defmodule ShopWeb.MailHTML do
      use ShopWeb, :html
      embed_templates "mail_html/*.html", suffix: "_html"
      embed_templates "mail_html/*.text", suffix: "_text"
    end
    ''',
    "lib/shop_web/mail_html/welcome.html.heex" => ~S'''
    <.icon />
    ''',
    "lib/shop_web/mail_html/welcome.text.heex" => ~S'''
    Welcome
    ''',
    "lib/shop_web/live/broken2.ex" => ~S'''
    defmodule ShopWeb.Broken2 do
      use ShopWeb, :html
      def bad(assigns), do: ~H|<%= foo|
    end
    ''',
    "lib/shop_web/live/broken.ex" => ~S'''
    defmodule ShopWeb.Broken do
      use ShopWeb, :html
      def bad(assigns), do: ~H|<div class="x></div>|
    end
    '''
  }

  defp root do
    root = Path.join(System.tmp_dir!(), "heex-#{System.unique_integer([:positive])}")
    on_exit(fn -> File.rm_rf!(root) end)

    for {path, text} <- @files do
      File.mkdir_p!(Path.dirname(Path.join(root, path)))
      File.write!(Path.join(root, path), text)
    end

    root
  end

  setup_all do
    root = Path.join(System.tmp_dir!(), "heex-all-#{System.unique_integer([:positive])}")

    for {path, text} <- @files do
      File.mkdir_p!(Path.dirname(Path.join(root, path)))
      File.write!(Path.join(root, path), text)
    end

    doc = ElixirFacts.build(repo: "x", root: root)
    File.rm_rf!(root)
    {:ok, doc: doc}
  end

  defp short(id), do: String.replace_prefix(id, "x:", "")

  defp sends(doc) do
    for e <- doc.events,
        do:
          {short(e.from), e[:event] || :dynamic, e.via, e[:target] || "",
           Enum.map(e[:to] || [], &short/1), e[:precision]}
  end

  test "component calls resolve per app, through use helpers", %{doc: doc} do
    comps =
      for c <- doc.calls,
          c.kind == "component",
          do: {short(c.from), c.target[:module], c.target.name}

    assert {"ShopWeb.PageLive.render/1", "ShopWeb.CoreComponents", "button"} in comps
    assert {"ShopWeb.PageLive.render/1", "ShopWeb.CoreComponents", "icon"} in comps
    assert {"OtherWeb.XLive.render/1", "OtherWeb.CoreComponents", "icon"} in comps
    assert {"ShopWeb.PageLive.render/1", "ShopWeb.RowComponent", "render"} in comps
    assert {"ShopWeb.PageHTML.home/1", "ShopWeb.CoreComponents", "button"} in comps
    # Code in {...} is walked as code: format/1 is a local call of render.
    assert Enum.any?(
             doc.calls,
             &(short(&1.from) == "ShopWeb.PageLive.render/1" and &1.target.name == "format" and
                 &1.kind == "local")
           )
  end

  test "template files become functions of their module", %{doc: doc} do
    home = Enum.find(doc.functions, &(&1.id == "x:ShopWeb.PageHTML.home/1"))
    assert home.file == "lib/shop_web/controllers/page_html/home.html.heex"

    assert Enum.find(doc.functions, &(&1.id == "x:ShopWeb.FoundersLive.render/1")).file ==
             "lib/shop_web/live/founders.html.heex"

    assert %{path: "lib/shop_web/live/founders.html.heex", language: "heex"} =
             Enum.find(doc.files, &String.ends_with?(&1.path, "founders.html.heex"))
  end

  test "events link to the handle_event that receives them", %{doc: doc} do
    page = "ShopWeb.PageLive.handle_event/3"
    s = sends(doc)

    # A LiveView's own template: precise.
    assert {"ShopWeb.PageLive.render/1", "save", "phx-click", "", [page], "precise"} in s
    # A component's pass-through attribute, named by the caller: heuristic.
    assert {"ShopWeb.PageLive.render/1", "delete", "phx-click", "", [page], "heuristic"} in s
    # Branch results, never the condition's literal.
    assert {"ShopWeb.PageLive.render/1", "open", "phx-click", "", [page], "precise"} in s
    assert {"ShopWeb.PageLive.render/1", "close", "phx-click", "", [page], "precise"} in s
    assert {"ShopWeb.PageLive.render/1", "go", "phx-click", "", [page], "precise"} in s
    refute Enum.any?(s, &(elem(&1, 1) == "payouts"))
    # JS.push in a chain.
    assert {"ShopWeb.PageLive.render/1", "blurred", "JS.push", "", [page], "precise"} in s
    # Interpolated names and unfilled assigns are dynamic; lv: events are LiveView's.
    assert Enum.count(s, &match?({"ShopWeb.PageLive.render/1", :dynamic, _, _, [], nil}, &1)) == 5
    assert {"ShopWeb.CoreComponents.unfilled/1", :dynamic, "phx-submit", "", [], nil} in s
    refute Enum.any?(doc.events, &String.starts_with?(&1[:event] || "", "lv:"))
  end

  test "phx-target={@myself} goes to the component; untargeted goes to the LiveView", %{doc: doc} do
    s = sends(doc)

    assert {"ShopWeb.RowComponent.render/1", "toggle", "phx-click", "myself",
            ["ShopWeb.RowComponent.handle_event/3"], "precise"} in s

    assert {"ShopWeb.RowComponent.render/1", "save", "phx-click", "",
            ["ShopWeb.PageLive.handle_event/3"], "heuristic"} in s
  end

  test "a delegating catch-all, and a send with no receiver", %{doc: doc} do
    s = sends(doc)

    assert {"ShopWeb.RevenueLive.render/1", "export", "phx-click", "",
            ["ShopWeb.Report.handle_event/3", "ShopWeb.RevenueLive.handle_event/3"], "heuristic"} in s

    # home.html.heex is rendered by no LiveView: counted, not linked.
    assert {"ShopWeb.PageHTML.home/1", "from_home", "phx-click", "", [], "heuristic"} in s
  end

  test "colocated hook pushEvent and hook mounts", %{doc: doc} do
    assert {"ShopWeb.FoundersLive.render/1", "signed", "pushEvent", "",
            ["ShopWeb.FoundersLive.handle_event/3"], "precise"} in sends(doc)

    mounts =
      for m <- doc.hook_mounts,
          do: {m.hook, short(m.from), Enum.map(m[:receivers] || [], &short/1)}

    assert {"Pad", "ShopWeb.PageLive.render/1", ["ShopWeb.PageLive.handle_event/3"]} in mounts

    assert {"ShopWeb.FoundersLive.Sig", "ShopWeb.FoundersLive.render/1",
            ["ShopWeb.FoundersLive.handle_event/3"]} in mounts
  end

  test "handler names: heads, guards, attributes, delegates", %{doc: doc} do
    hs =
      for h <- doc.event_handlers,
          short(h.function) == "ShopWeb.PageLive.handle_event/3",
          do: {h[:event], h.source}

    assert Enum.sort(hs) ==
             Enum.sort([
               {"save", "head"},
               {"delete", "head"},
               {"open", "guard"},
               {"close", "guard"},
               {"a1", "attribute"},
               {"a2", "attribute"},
               {"go", "head"},
               {"blurred", "head"},
               {"dead", "head"},
               {"b1", "attribute"}
             ])

    assert [%{source: "delegate", delegate: "x:ShopWeb.Report.handle_event/3"}] =
             Enum.filter(
               doc.event_handlers,
               &(short(&1.function) == "ShopWeb.RevenueLive.handle_event/3")
             )
  end

  test "a broken template is a note; the counts add up", %{doc: doc} do
    assert Enum.any?(
             doc.notes,
             &(&1.kind == "heex_parse_error" and &1.file == "lib/shop_web/live/broken.ex")
           )

    heex = Enum.find(doc.stats, &(&1.language == "heex"))
    assert heex.call_sites == length(doc.events)
    assert heex.call_sites == heex.in_repo + heex.unresolved + heex.not_extracted
    assert heex.not_extracted == 7
    assert doc.facts_version == 3
  end

  test "the document validates as facts version 3", %{doc: _doc} do
    r = root()

    out =
      System.cmd("elixir", [Path.expand("../extract.exs", __DIR__), "--repo", "x", "--root", r],
        stderr_to_stdout: false
      )

    assert {json, 0} = out
    assert %{"facts_version" => 3, "events" => [_ | _]} = JSON.decode!(json)
  end

  test "review round 1: JS.push target by name, pushEventTo, attribute calls, embed suffix, EEx errors",
       %{doc: doc} do
    s = sends(doc)

    assert {"ShopWeb.RowComponent.render/1", "pick", "JS.push", "myself",
            ["ShopWeb.RowComponent.handle_event/3"], "precise"} in s

    assert {"ShopWeb.FoundersLive.render/1", "aimed", "pushEventTo", "dynamic", [], "heuristic"} in s

    assert Enum.any?(
             doc.calls,
             &(short(&1.from) == "ShopWeb.PageLive.render/1" and &1.target.name == "show_modal" and
                 &1.kind == "local")
           )

    assert Enum.find(doc.functions, &(&1.id == "x:ShopWeb.MailHTML.welcome_text/1")).file ==
             "lib/shop_web/mail_html/welcome.text.heex"

    assert Enum.find(doc.functions, &(&1.id == "x:ShopWeb.MailHTML.welcome_html/1")).file ==
             "lib/shop_web/mail_html/welcome.html.heex"

    refute Enum.any?(doc.notes, &(&1.kind == "heex_unowned"))

    assert Enum.any?(
             doc.notes,
             &(&1.kind == "heex_parse_error" and &1.file == "lib/shop_web/live/broken2.ex" and
                 String.starts_with?(&1.message, "line "))
           )
  end

  test "review round 3: script comments and strings, per-call pass-through, empty names, schema v3",
       %{doc: doc} do
    s = sends(doc)
    founders = for {"ShopWeb.FoundersLive.render/1", ev, _, _, _, _} <- s, do: ev

    assert Enum.sort(founders, &(inspect(&1) <= inspect(&2))) ==
             Enum.sort(
               [:dynamic, "after_raw", "aimed", "divided", "signed", "templated"],
               &(inspect(&1) <= inspect(&2))
             )

    refute Enum.any?(doc.events, &(&1[:event] in ["ghost", "inside", "", "row_"]))
    # <.button click="delete"> and <.button click={@event}>: one named, one dynamic.
    assert {"ShopWeb.PageLive.render/1", "delete", "phx-click", "",
            ["ShopWeb.PageLive.handle_event/3"], "heuristic"} in s

    refute Enum.any?(doc.event_handlers, &(&1[:event] == ""))

    r = root()
    schema = Path.join(r, "schema.json")
    File.write!(schema, ~s({"facts_version":3,"producer":{"name":"capture"},"tables":[]}))
    assert %{facts_version: 3} = ElixirFacts.build(repo: "x", root: r, schema_facts: schema)
  end
end
