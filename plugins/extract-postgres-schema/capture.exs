# extract-postgres-schema: writes one facts v1 schema document (tables,
# columns, constraints, indexes, foreign keys) captured from a disposable
# loopback PostgreSQL database to stdout. Usage: see README.md.
Code.require_file("capture_lib.exs", __DIR__)

usage = """
usage: elixir capture.exs --database NAME [--host 127.0.0.1] [--port 5432]
                          [--user postgres] [--container NAME]
                          [--db-prefix system_map_,other_] [--schemas public]
"""

case Cartographer.PgCapture.parse_args(System.argv()) do
  {:ok, opts} ->
    try do
      doc = Cartographer.PgCapture.capture(opts)
      IO.binwrite(:stdio, Cartographer.PgCapture.encode(doc))
    rescue
      e ->
        IO.puts(:stderr, "capture failed: " <> Exception.message(e))
        System.halt(1)
    end

  {:error, message} ->
    IO.puts(:stderr, message <> "\n" <> usage)
    System.halt(2)
end
