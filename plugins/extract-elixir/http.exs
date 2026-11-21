defmodule Cartographer.ElixirHTTP do
  @moduledoc """
  HTTP references through Req wrappers, from source only.

  A *carrier* is a function, in a module that calls `Req.request`/`get`/
  `post`/`put`/`patch`/`delete`/`head`, that passes one of its parameters
  on to such a call as the URL or inside its options (`url:`), directly or
  through `Req.new`/`Req.merge`/`Req.Request.*`/`Keyword.put`/`merge`/...,
  `++`, interpolation, variables, and calls to other carriers of the same
  module. Carriers are found by a fixpoint per module; return values are
  not followed.

  A call to a carrier whose carrier argument does not come from the
  caller's own parameters is where a request starts. With a string literal,
  an interpolated string, or a keyword list whose `url:` is one of those,
  it becomes an HTTP reference; anything else (a piped path, a variable, a
  call, a capture) is counted as not extracted. So is a Req request call
  whose URL comes from no parameter, since nothing names its path.
  """

  alias Cartographer.ElixirSource

  @verbs %{
    "get" => "GET",
    "post" => "POST",
    "put" => "PUT",
    "patch" => "PATCH",
    "delete" => "DELETE",
    "head" => "HEAD",
    "options" => "OPTIONS"
  }
  @keyword_through ~w(put put_new merge drop delete take update replace)
  @empty MapSet.new()

  defguardp ident?(x) when is_atom(x) or (is_map(x) and is_map_key(x, :__atom__))

  @doc """
  `clauses` are ElixirSource clauses (those of modules that call Req carry
  `:http`), `calls` raw calls with `:target_module` resolved, `functions`
  the merged functions. Returns `{refs, skipped}`, where each skipped site is
  `%{file, line, from, reason}`.
  """
  def refs(clauses, calls, functions) do
    defs =
      Enum.group_by(
        functions,
        fn f -> {f.module |> String.split(":", parts: 2) |> List.last(), f.name} end,
        &{&1.min_arity, &1.arity, &1.id}
      )

    analyses =
      clauses
      |> Enum.filter(&Map.has_key?(&1, :http))
      |> Enum.group_by(& &1.module)
      |> Enum.map(fn {module, cs} -> analyze(module, cs, defs) end)

    carriers =
      for a <- analyses,
          {fid, positions} <- a.carriers,
          into: %{},
          do: {fid, %{positions: positions, method: a.methods[fid], module: a.module}}

    pass = Enum.reduce(analyses, MapSet.new(), &MapSet.union(&2, &1.pass))
    direct = Enum.flat_map(analyses, & &1.direct)

    {refs, skipped} =
      Enum.reduce(calls, {[], []}, fn c, {refs, skipped} ->
        with module when is_binary(module) <- c.target_module,
             true <- c.kind in ["remote", "local", "import", "capture"],
             fid when fid != nil <- lookup(defs, module, c.name, c.arity),
             %{} = carrier <- carriers[fid],
             false <- MapSet.member?(pass, {c.from, c.line, c.name, c.arity}) do
          case origin(c, carrier) do
            {:ok, path, method} ->
              ref =
                %{from: c.from, path: path, base: carrier.module, file: c.file, line: c.line}
                |> then(&if(method, do: Map.put(&1, :method, method), else: &1))

              {[ref | refs], skipped}

            {:not_extracted, reason} ->
              {refs, [%{file: c.file, line: c.line, from: c.from, reason: reason} | skipped]}
          end
        else
          _ -> {refs, skipped}
        end
      end)

    refs =
      (refs ++ Enum.flat_map(analyses, & &1.direct_refs))
      |> Enum.uniq()
      |> Enum.sort_by(&{&1.file, &1.line, &1.from, &1.path, &1[:method] || ""})

    {refs, Enum.sort_by(skipped ++ direct, &{&1.file, &1.line})}
  end

  defp lookup(defs, module, name, arity) do
    defs
    |> Map.get({module, name}, [])
    |> Enum.find_value(fn {min, max, id} -> if arity in min..max, do: id end)
  end

  # --- call sites ------------------------------------------------------------

  defp origin(%{kind: "capture"}, _carrier), do: {:not_extracted, "capture"}

  defp origin(c, carrier) do
    args = c.args || []

    results =
      carrier.positions
      |> Enum.filter(&(&1 < length(args)))
      |> Enum.sort()
      |> Enum.map(&path_of(Enum.at(args, &1)))

    case Enum.find(results, &match?({:ok, _, _}, &1)) do
      {:ok, path, method} ->
        {:ok, path, method || method_option(args) || carrier.method || verb_argument(args)}

      nil ->
        {:not_extracted, if(:piped in results, do: "piped", else: "not a literal path")}
    end
  end

  # The pipe placeholder ElixirSource puts first in a piped call: the path
  # its left side names ("/x" |> get()), else :piped.
  defp path_of({:__pipe_arg__, meta, nil}) do
    case template(Keyword.get(meta, :value)) do
      {:ok, path} -> {:ok, path, nil}
      :none -> :piped
    end
  end

  defp path_of(list) when is_list(list) do
    with true <- keyword?(list),
         {_, url} <- Enum.find(list, fn {k, _} -> ElixirSource.str(k) == "url" end),
         {:ok, path} <- template(url) do
      {:ok, path, method_option([list])}
    else
      _ -> :none
    end
  end

  defp path_of(expr) do
    case template(expr) do
      {:ok, path} -> {:ok, path, nil}
      :none -> :none
    end
  end

  # The path a string expression names: a literal, an interpolation, or a
  # `<>` of those and other expressions. The query string is dropped; it
  # must start with a literal /. A segment holding an interpolation becomes
  # :p1, :p2, ...; any other expression must be a whole segment to do so
  # (`"/x/" <> id`, not `"/search" <> query`). A path with parameters needs
  # a literal segment too: `"/#{path}"` names no path.
  defp template(expr) do
    text =
      expr
      |> parts()
      |> Enum.map_join(fn
        :interp -> <<0>>
        :opaque -> <<1>>
        text -> text
      end)
      |> String.split("?", parts: 2)
      |> hd()

    with true <- String.starts_with?(text, "/"),
         {segments, n} <- text |> String.split("/") |> Enum.map_reduce(1, &segment/2),
         false <- :none in segments,
         true <- n == 1 or Enum.any?(segments, &(&1 != "" and not String.starts_with?(&1, ":"))) do
      {:ok, Enum.join(segments, "/")}
    else
      _ -> :none
    end
  end

  defp segment(<<1>>, n), do: {":p#{n}", n + 1}

  defp segment(text, n) do
    cond do
      String.contains?(text, <<1>>) -> {:none, n}
      String.contains?(text, <<0>>) -> {":p#{n}", n + 1}
      true -> {text, n}
    end
  end

  defp parts({:<>, _, [left, right]}), do: parts(left) ++ parts(right)
  defp parts(text) when is_binary(text), do: [text]

  defp parts({:<<>>, _, ps}) when is_list(ps),
    do: Enum.map(ps, &if(is_binary(&1), do: &1, else: :interp))

  defp parts(_), do: [:opaque]

  # A literal `method: :post` in a call's keyword arguments.
  defp method_option(args) do
    Enum.find_value(args, fn arg ->
      if is_list(arg) and keyword?(arg) do
        Enum.find_value(arg, fn
          {k, v} when ident?(v) and not is_nil(v) and not is_boolean(v) ->
            if ElixirSource.str(k) == "method", do: String.upcase(ElixirSource.str(v))

          _ ->
            nil
        end)
      end
    end)
  end

  # `request(:get, path)`: a literal verb among the arguments, for a wrapper
  # whose method is a parameter.
  defp verb_argument(args) do
    Enum.find_value(args, fn
      arg when ident?(arg) and not is_nil(arg) and not is_boolean(arg) ->
        Map.get(@verbs, ElixirSource.str(arg))

      _ ->
        nil
    end)
  end

  defp keyword?(list), do: list != [] and Enum.all?(list, &match?({k, _} when ident?(k), &1))

  # --- carriers ----------------------------------------------------------------

  defp analyze(module, clauses, defs) do
    local = fn name, arity -> lookup(defs, module, name, arity) end

    carriers = fixpoint(%{}, clauses, module, local)

    runs = Enum.map(clauses, &run(&1, module, local, carriers))

    evidence =
      Enum.reduce(runs, %{}, fn st, acc ->
        Map.update(acc, st.from, st.evidence, &(&1 ++ st.evidence))
      end)

    names = Map.new(clauses, &{&1.id, &1.name})

    %{
      module: module,
      carriers: carriers,
      methods:
        Map.new(carriers, fn {fid, _} -> {fid, method(fid, names, evidence, MapSet.new())} end),
      pass: runs |> Enum.flat_map(& &1.pass) |> MapSet.new(),
      direct: Enum.flat_map(runs, & &1.direct),
      direct_refs: Enum.flat_map(runs, & &1.direct_refs)
    }
  end

  # Carrier positions per function id, grown until no clause adds any.
  defp fixpoint(carriers, clauses, module, local) do
    next =
      Enum.reduce(clauses, carriers, fn c, acc ->
        st = run(c, module, local, carriers)

        if MapSet.size(st.carrier) == 0,
          do: acc,
          else: Map.update(acc, c.id, st.carrier, &MapSet.union(&1, st.carrier))
      end)

    if next == carriers, do: carriers, else: fixpoint(next, clauses, module, local)
  end

  # A carrier's method: its name when that is a verb, else the one method
  # every request it passes its path to agrees on (a recursive call adds
  # nothing).
  defp method(fid, names, evidence, seen) do
    case Map.fetch(@verbs, names[fid]) do
      {:ok, verb} ->
        verb

      :error ->
        seen = MapSet.put(seen, fid)

        methods =
          evidence
          |> Map.get(fid, [])
          |> Enum.flat_map(fn
            {:lit, m} ->
              [m]

            {:fid, g} ->
              if MapSet.member?(seen, g), do: [], else: [method(g, names, evidence, seen)]

            :unknown ->
              [nil]
          end)
          |> Enum.uniq()

        case methods do
          [m] when is_binary(m) -> m
          _ -> nil
        end
    end
  end

  defp run(c, module, local, carriers) do
    env =
      for {param, i} <- Enum.with_index(c.http.params),
          v <- vars(param),
          into: %{},
          do: {v, MapSet.new([i])}

    st = %{
      env: env,
      module: module,
      aliases: c.http.aliases,
      local: local,
      carriers: carriers,
      from: c.id,
      file: c.file,
      carrier: @empty,
      pass: [],
      direct: [],
      direct_refs: [],
      evidence: []
    }

    {_, st} = ev(c.http.body, st)
    st
  end

  # Evaluates `expr` for the parameters its value carries; records carriers,
  # pass-through calls and direct requests on the way.
  defp ev({:__block__, _, exprs}, st) when is_list(exprs), do: ev_seq(exprs, st)

  defp ev({op, _, [left, right]}, st) when op in [:=, :<-] do
    {t, st} = ev(right, st)
    {t, Enum.reduce(vars(left), st, fn v, st -> %{st | env: Map.put(st.env, v, t)} end)}
  end

  defp ev({:|>, _, [left, right]}, st) do
    case right do
      {{:., _, _} = dot, meta, args} when is_list(args) ->
        ev({dot, meta, [left | args]}, st)

      {fun, meta, args} when ident?(fun) and is_list(args) ->
        ev({fun, meta, [left | args]}, st)

      _ ->
        {_, st} = ev(left, st)
        {_, st} = ev(right, st)
        {@empty, st}
    end
  end

  defp ev({:++, _, [a, b]}, st) do
    {ta, st} = ev(a, st)
    {tb, st} = ev(b, st)
    {MapSet.union(ta, tb), st}
  end

  # A string that already names a path (template/1) starts a request where
  # it is written: the parameters in it are path parameters, not the path.
  # Otherwise (`"#{base}#{path}"`) it carries what its parts carry.
  defp ev({:<<>>, _, parts} = expr, st) when is_list(parts) do
    {t, st} =
      Enum.reduce(parts, {@empty, st}, fn
        {:"::", _, [{{:., _, _}, _, [inner]}, _]}, {t, st} ->
          {ti, st} = ev(inner, st)
          {MapSet.union(t, ti), st}

        part, {t, st} ->
          {_, st} = ev(part, st)
          {t, st}
      end)

    {if(template(expr) == :none, do: t, else: @empty), st}
  end

  defp ev({:<>, _, [a, b]} = expr, st) do
    {ta, st} = ev(a, st)
    {tb, st} = ev(b, st)
    {if(template(expr) == :none, do: MapSet.union(ta, tb), else: @empty), st}
  end

  defp ev({name, _, ctx}, st) when ident?(name) and is_atom(ctx),
    do: {Map.get(st.env, ElixirSource.str(name), @empty), st}

  defp ev({{:., _, [mod, fun]}, meta, args}, st) when ident?(fun) and is_list(args) do
    {_, st} = ev(mod, st)
    {taints, st} = ev_args(args, st)

    module =
      if match?({:__aliases__, _, _}, mod) or match?({:__MODULE__, _, _}, mod),
        do: ElixirSource.module_name(mod, %{module: st.module, aliases: st.aliases})

    name = ElixirSource.str(fun)

    cond do
      module == "Req" and name in ElixirSource.req_sinks() ->
        {@empty, sink(name, meta, args, taints, st)}

      (module == "Req" and name in ["new", "merge"]) or module == "Req.Request" or
          (module == "Keyword" and name in @keyword_through) ->
        {union(taints), st}

      module == st.module ->
        {@empty, local_call(name, meta, args, taints, st)}

      true ->
        {@empty, st}
    end
  end

  defp ev({fun, meta, args}, st) when ident?(fun) and is_list(args) and is_list(meta) do
    {taints, st} = ev_args(args, st)

    if st.local.(ElixirSource.str(fun), length(args)),
      do: {@empty, local_call(ElixirSource.str(fun), meta, args, taints, st)},
      else: {@empty, st}
  end

  defp ev({a, meta, args}, st) when is_list(meta) do
    {_, st} = ev(a, st)
    {_, st} = ev(args, st)
    {@empty, st}
  end

  defp ev({a, b}, st) do
    {_, st} = ev(a, st)
    {_, st} = ev(b, st)
    {@empty, st}
  end

  # A keyword list carries what its `url:` carries.
  defp ev(list, st) when is_list(list) do
    if keyword?(list) do
      Enum.reduce(list, {@empty, st}, fn {k, v}, {t, st} ->
        {tv, st} = ev(v, st)
        {if(ElixirSource.str(k) == "url", do: MapSet.union(t, tv), else: t), st}
      end)
    else
      {_, st} = ev_seq(list, st)
      {@empty, st}
    end
  end

  defp ev(_, st), do: {@empty, st}

  defp ev_seq(exprs, st), do: Enum.reduce(exprs, {@empty, st}, fn e, {_, st} -> ev(e, st) end)

  defp ev_args(args, st), do: Enum.map_reduce(args, st, &ev/2)

  defp union(taints), do: Enum.reduce(taints, @empty, &MapSet.union/2)

  # A Req request call. A literal path among its arguments makes it a
  # reference here; else, when it carries this function's parameters, the
  # function is a carrier; else it is a request nothing names a path for.
  defp sink(name, meta, args, taints, st) do
    carried = union(taints)
    verb = String.trim_trailing(name, "!")
    method = Map.get(@verbs, verb) || method_option(args)
    line = Keyword.get(meta, :line, 1)

    found =
      Enum.find_value(args, fn arg ->
        case path_of(arg) do
          {:ok, path, _} -> path
          _ -> nil
        end
      end)

    cond do
      found ->
        ref =
          %{from: st.from, path: found, base: st.module, file: st.file, line: line}
          |> then(&if(method, do: Map.put(&1, :method, method), else: &1))

        %{st | direct_refs: [ref | st.direct_refs]}

      MapSet.size(carried) == 0 ->
        site = %{file: st.file, line: line, from: st.from, reason: "Req.#{name} without a path"}
        %{st | direct: [site | st.direct]}

      true ->
        evidence = if method, do: {:lit, method}, else: :unknown
        %{st | carrier: MapSet.union(st.carrier, carried), evidence: [evidence | st.evidence]}
    end
  end

  # A call to another function of this module: when it hands a carrier
  # position something from this function's parameters (and no literal
  # path), this function is a carrier too and the call only passes the
  # path through.
  defp local_call(name, meta, args, taints, st) do
    arity = length(taints)
    fid = st.local.(name, arity)
    positions = st.carriers |> Map.get(fid, @empty) |> Enum.filter(&(&1 < arity))

    passed =
      if Enum.any?(positions, &match?({:ok, _, _}, path_of(Enum.at(args, &1)))),
        do: @empty,
        else: Enum.reduce(positions, @empty, &MapSet.union(&2, Enum.at(taints, &1)))

    if MapSet.size(passed) == 0 do
      st
    else
      %{
        st
        | carrier: MapSet.union(st.carrier, passed),
          pass: [{st.from, Keyword.get(meta, :line, 1), name, arity} | st.pass],
          evidence: [{:fid, fid} | st.evidence]
      }
    end
  end

  # Variables a pattern binds (a default's pattern, not its value).
  defp vars({:\\, _, [pattern, _default]}), do: vars(pattern)
  defp vars({:^, _, _}), do: []
  defp vars({:__aliases__, _, _}), do: []

  defp vars({name, _, ctx}) when ident?(name) and is_atom(ctx) do
    n = ElixirSource.str(name)
    if String.starts_with?(n, "_"), do: [], else: [n]
  end

  defp vars({a, meta, args}) when is_list(meta), do: vars(a) ++ vars(args)
  defp vars({a, b}), do: vars(a) ++ vars(b)
  defp vars(list) when is_list(list), do: Enum.flat_map(list, &vars/1)
  defp vars(_), do: []
end
