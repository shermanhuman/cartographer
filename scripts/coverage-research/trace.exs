# The compiler oracle (map-coverage-2 decisions 21, 24, 26). Run inside the
# staged project: compiles deps, then the project with a call tracer, then
# reads each project module's debug info for the library that generated a
# definition (`context:`). Writes oracle.json.
defmodule OracleTracer do
  @kinds [:remote_function, :remote_macro, :imported_function, :imported_macro, :local_function, :local_macro]
  def trace({kind, meta, module, name, arity}, env) when kind in @kinds do
    :ets.insert(:oracle, {env.file, meta[:line], Atom.to_string(name), arity, Atom.to_string(kind), inspect(module)})
    :ok
  end

  def trace({kind, meta, name, arity}, env) when kind in [:local_function, :local_macro] do
    :ets.insert(:oracle, {env.file, meta[:line], Atom.to_string(name), arity, Atom.to_string(kind), inspect(env.module)})
    :ok
  end

  def trace(_, _), do: :ok
end

:ets.new(:oracle, [:duplicate_bag, :public, :named_table])
Mix.start()
Mix.env(:dev)
Code.compile_file("mix.exs")
project = Mix.Project.get!()
Mix.Task.run("loadconfig")
Mix.Task.run("deps.compile")
Code.put_compiler_option(:tracers, [OracleTracer | Code.get_compiler_option(:tracers)])
Mix.Task.run("compile", ["--no-deps-check", "--force"])

app = Mix.Project.config()[:app]
ebin = Path.join(Mix.Project.build_path(), "lib/#{app}/ebin")

contexts =
  for beam <- Path.wildcard(Path.join(ebin, "*.beam")),
      {:ok, {mod, [debug_info: {:debug_info_v1, backend, data}]}} <- [:beam_lib.chunks(String.to_charlist(beam), [:debug_info])],
      {:ok, %{definitions: defs}} <- [backend.debug_info(:elixir_v1, mod, data, [])],
      {{name, arity}, _kind, meta, _clauses} <- defs,
      ctx = meta[:context],
      ctx != nil do
    %{module: inspect(mod), name: Atom.to_string(name), arity: arity, context: inspect(ctx)}
  end

root = File.cwd!()
calls =
  for {file, line, name, arity, kind, module} <- :ets.tab2list(:oracle) do
    %{file: Path.relative_to(file, root), line: line, name: name, arity: arity, kind: kind, module: module}
  end

File.write!("oracle.json", JSON.encode!(%{calls: calls, contexts: contexts}))
IO.puts("oracle: #{length(calls)} trace events, #{length(contexts)} definitions with a context")
_ = project
