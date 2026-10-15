defmodule Cartographer.PgCapture do
  @moduledoc """
  Metadata-only PostgreSQL schema capture from an explicitly named,
  disposable loopback database, emitted as a facts v1 schema document.

  Guards (all enforced before any query):
    * the host is loopback (127.0.0.1, localhost, ::1);
    * the database name starts with an allowed prefix (`cartographer_`
      plus any `--db-prefix`);
    * every session is read-only with a 30 s statement timeout, and the
      read-only setting is verified before the catalog query runs;
    * the server is PostgreSQL 15 or newer (18 is the tested version; others
      warn).
  """

  @version "0.1.0"
  @default_prefixes ["cartographer_"]
  @loopback ["127.0.0.1", "localhost", "::1"]
  @pgoptions "-c default_transaction_read_only=on -c statement_timeout=30000"
  @tested_major 18
  @minimum_major 15
  @identifier ~r/\A[a-z_][a-z0-9_]*\z/

  @constraint_kinds %{
    "p" => "primary_key",
    "u" => "unique",
    "f" => "foreign_key",
    "c" => "check",
    "x" => "exclusion",
    "t" => "trigger"
  }

  def pgoptions, do: @pgoptions

  @switches [
    database: :string,
    host: :string,
    port: :integer,
    user: :string,
    container: :string,
    db_prefix: :keep,
    schemas: :string,
    help: :boolean
  ]

  @doc "Parses and validates CLI arguments. Returns {:ok, opts} or {:error, message}."
  def parse_args(argv) do
    {opts, rest, invalid} = OptionParser.parse(argv, strict: @switches)

    with :ok <-
           if(rest == [] and invalid == [],
             do: :ok,
             else: {:error, "unknown arguments: #{inspect(rest ++ invalid)}"}
           ),
         {:ok, database} <- fetch(opts, :database),
         host = opts[:host] || "127.0.0.1",
         :ok <- check_host(host),
         {:ok, prefixes} <- prefixes(Keyword.get_values(opts, :db_prefix)),
         :ok <- check_database(database, prefixes),
         {:ok, schemas} <- schemas(opts[:schemas] || "public"),
         :ok <- check_container(opts[:container]),
         :ok <- check_user(opts[:user] || "postgres") do
      {:ok,
       %{
         database: database,
         host: host,
         port: opts[:port] || 5432,
         user: opts[:user] || "postgres",
         container: opts[:container],
         schemas: schemas
       }}
    end
  end

  defp fetch(opts, key) do
    case opts[key] do
      nil -> {:error, "--#{key} is required"}
      value -> {:ok, value}
    end
  end

  @doc "Only loopback hosts are allowed."
  def check_host(host) when host in @loopback, do: :ok

  def check_host(host),
    do:
      {:error,
       "--host #{host} is not loopback; schema capture requires a disposable local database"}

  @doc "Allowed name prefixes: the defaults plus each comma-separated --db-prefix value."
  def prefixes(values) do
    extra = values |> Enum.flat_map(&String.split(&1, ",")) |> Enum.map(&String.trim/1)

    case Enum.find(extra, &(not Regex.match?(@identifier, &1))) do
      nil ->
        {:ok, Enum.uniq(@default_prefixes ++ extra)}

      bad ->
        {:error, "--db-prefix #{inspect(bad)} must be a non-empty lowercase identifier prefix"}
    end
  end

  @doc "The database must be a plain lowercase name starting with an allowed prefix."
  def check_database(name, prefixes) do
    cond do
      not Regex.match?(~r/\A[a-z0-9_]+\z/, name) ->
        {:error, "--database #{inspect(name)} must be a lowercase identifier"}

      not Enum.any?(prefixes, &String.starts_with?(name, &1)) ->
        {:error,
         "--database #{name} does not start with an allowed disposable prefix (#{Enum.join(prefixes, ", ")}); add one with --db-prefix"}

      true ->
        :ok
    end
  end

  def schemas(value) do
    list = value |> String.split(",") |> Enum.map(&String.trim/1) |> Enum.reject(&(&1 == ""))

    cond do
      list == [] ->
        {:error, "--schemas is empty"}

      bad = Enum.find(list, &(not Regex.match?(@identifier, &1))) ->
        {:error, "--schemas #{inspect(bad)} is not a plain identifier"}

      true ->
        {:ok, Enum.uniq(list)}
    end
  end

  defp check_container(nil), do: :ok

  defp check_container(name) do
    if Regex.match?(~r/\A[A-Za-z0-9][A-Za-z0-9_.-]*\z/, name),
      do: :ok,
      else: {:error, "--container #{inspect(name)} is not a container name"}
  end

  defp check_user(user) do
    if Regex.match?(~r/\A[A-Za-z0-9_][A-Za-z0-9_.-]*\z/, user),
      do: :ok,
      else: {:error, "--user #{inspect(user)} is not a role name"}
  end

  @doc "Version check on `SHOW server_version_num`: :ok, {:warn, msg} or {:error, msg}."
  def check_version(num) when is_integer(num) do
    major = div(num, 10_000)

    cond do
      major < @minimum_major ->
        {:error, "PostgreSQL #{major} is too old; use #{@minimum_major} or newer"}

      major != @tested_major ->
        {:warn, "PostgreSQL #{major} is untested; captures are tested on #{@tested_major}"}

      true ->
        :ok
    end
  end

  @doc "The read-only setting must have reached the session."
  def check_read_only("on"), do: :ok

  def check_read_only(other),
    do:
      {:error,
       "session is not read-only (default_transaction_read_only=#{other}); refusing to capture"}

  @doc "Catalog SQL with the validated schema list substituted."
  def sql(schemas) do
    list = Enum.map_join(schemas, ",", &"'#{&1}'")
    __DIR__ |> Path.join("schema.sql") |> File.read!() |> String.replace("__SCHEMAS__", list)
  end

  @doc "{command, args, env} for one psql query. Never goes through a shell."
  def psql_command(opts, sql) do
    connection =
      if opts.container, do: [], else: ["--host", opts.host, "--port", to_string(opts.port)]

    args =
      ["--no-psqlrc"] ++
        connection ++
        [
          "--username",
          opts.user,
          "--dbname",
          opts.database,
          "--no-password",
          "--tuples-only",
          "--no-align",
          "--set",
          "ON_ERROR_STOP=1",
          "--command",
          sql
        ]

    env = [{"PGOPTIONS", @pgoptions}]

    if opts.container,
      do:
        {"docker", ["exec", "--env", "PGOPTIONS=#{@pgoptions}", opts.container, "psql" | args],
         env},
      else: {"psql", args, env}
  end

  @doc "Runs one query and returns trimmed stdout."
  def query(opts, sql) do
    {command, args, env} = psql_command(opts, sql)

    unless System.find_executable(command),
      do: raise("#{command} is not on PATH")

    # psql diagnostics go to our stderr; stdout carries only the result.
    case System.cmd(command, args, env: env) do
      {out, 0} ->
        String.trim(out)

      {_, status} ->
        raise("catalog query failed (#{command} exit #{status}); see psql output above")
    end
  end

  @doc "Converts the schema.sql result into a facts v1 schema document."
  def to_facts(raw) do
    tables =
      raw["tables"]
      |> Enum.map(fn t ->
        %{
          id: t["id"],
          schema: t["schema"],
          name: t["name"],
          columns:
            Enum.map(t["columns"] || [], fn c ->
              base = %{name: c["name"], type: c["type"], nullable: c["nullable"]}
              if c["default"], do: Map.put(base, :default, c["default"]), else: base
            end),
          constraints:
            for(
              c <- t["constraints"] || [],
              kind = @constraint_kinds[c["type"]],
              do: %{name: c["name"], kind: kind, definition: c["definition"]}
            )
            |> Enum.sort_by(& &1.name),
          indexes: (t["indexes"] || []) |> Enum.map(& &1["name"]) |> Enum.sort()
        }
      end)
      |> Enum.sort_by(& &1.id)

    ids = MapSet.new(tables, & &1.id)

    {fks, outside} =
      (raw["foreign_keys"] || [])
      |> Enum.map(fn fk ->
        %{
          name: fk["name"],
          from_table: fk["from"],
          from_columns: fk["columns"],
          to_table: fk["to"],
          to_columns: fk["target_columns"]
        }
      end)
      |> Enum.split_with(
        &(MapSet.member?(ids, &1.from_table) and MapSet.member?(ids, &1.to_table))
      )

    notes =
      for fk <- outside,
          do: %{
            kind: "uncaptured_foreign_key_target",
            message:
              "#{fk.name}: #{fk.from_table} references #{fk.to_table}, which is outside the captured schemas"
          }

    %{
      facts_version: 1,
      producer: %{name: "extract-postgres-schema", version: @version},
      tables: tables,
      foreign_keys: Enum.sort_by(fks, &{&1.from_table, &1.name}),
      notes: Enum.sort_by(notes, & &1.message)
    }
    |> Enum.reject(fn {_, v} -> v == [] end)
    |> Map.new()
  end

  # Deterministic key order, matching docs/facts-format.md.
  @key_order ~w(facts_version producer tables foreign_keys notes id schema name version kind
                from_table from_columns to_table to_columns type nullable default definition)

  def encode(term), do: term |> encode_iodata() |> IO.iodata_to_binary()

  defp encode_iodata(map) when is_map(map) do
    pairs =
      map
      |> Enum.map(fn {k, v} -> {to_string(k), v} end)
      |> Enum.sort_by(fn {k, _} ->
        {Enum.find_index(@key_order, &(&1 == k)) || length(@key_order), k}
      end)
      |> Enum.map(fn {k, v} -> [JSON.encode!(k), ?:, encode_iodata(v)] end)

    [?{, Enum.intersperse(pairs, ?,), ?}]
  end

  defp encode_iodata(list) when is_list(list),
    do: [?[, list |> Enum.map(&encode_iodata/1) |> Enum.intersperse(?,), ?]]

  defp encode_iodata(other), do: JSON.encode!(other)

  @doc "Runs every guard, then captures. Returns the facts document."
  def capture(opts, warn \\ &IO.puts(:stderr, &1)) do
    version = opts |> query("SHOW server_version_num") |> String.to_integer()

    case check_version(version) do
      :ok -> :ok
      {:warn, message} -> warn.("warning: " <> message)
      {:error, message} -> raise(message)
    end

    case check_read_only(query(opts, "SHOW default_transaction_read_only")) do
      :ok -> :ok
      {:error, message} -> raise(message)
    end

    opts |> query(sql(opts.schemas)) |> JSON.decode!() |> to_facts()
  end
end
