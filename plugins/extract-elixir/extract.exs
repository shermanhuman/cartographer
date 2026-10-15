# extract-elixir: writes one facts v1 document for an Elixir repository to
# stdout. Usage: see README.md. Never compiles or executes project code.
Code.require_file("source.exs", __DIR__)
Code.require_file("facts.exs", __DIR__)

defmodule Cartographer.ElixirExtractCLI do
  @usage """
  usage: elixir extract.exs --repo NAME --root DIR [--revision SHA]
                            [--source-roots lib,test] [--schema-facts FILE]
                            [--ref-roots lib,priv/repo/migrations,scripts]
  """

  def main(argv) do
    {opts, rest, invalid} =
      OptionParser.parse(argv,
        strict: [
          repo: :string,
          root: :string,
          revision: :string,
          source_roots: :string,
          ref_roots: :string,
          schema_facts: :string,
          help: :boolean
        ]
      )

    if opts[:help], do: halt(@usage, 0)

    if rest != [] or invalid != [],
      do: halt("unknown arguments: #{inspect(rest ++ invalid)}\n" <> @usage)

    repo = opts[:repo] || halt("--repo is required\n" <> @usage)
    root = opts[:root] || halt("--root is required\n" <> @usage)

    unless Regex.match?(~r/\A[A-Za-z0-9][A-Za-z0-9._-]*\z/, repo),
      do: halt("--repo must be a plain name (letters, digits, . _ -)")

    unless File.dir?(root), do: halt("--root #{root} is not a directory")

    schema = opts[:schema_facts]
    if schema && not File.regular?(schema), do: halt("--schema-facts #{schema} is not a file")

    doc =
      Cartographer.ElixirFacts.build(
        repo: repo,
        root: root,
        revision: opts[:revision],
        source_roots: roots(opts[:source_roots], "--source-roots"),
        ref_roots: roots(opts[:ref_roots], "--ref-roots"),
        schema_facts: schema
      )

    IO.binwrite(:stdio, Cartographer.ElixirFacts.encode(doc))
  end

  defp roots(nil, _), do: nil

  defp roots(value, flag) do
    list =
      value
      |> String.split(",", trim: true)
      |> Enum.map(&String.trim/1)
      |> Enum.reject(&(&1 == ""))

    for r <- list,
        Path.type(r) != :relative or ".." in Path.split(r),
        do: halt("#{flag} entries must be relative paths inside --root: #{r}")

    if list == [], do: halt("#{flag} is empty")
    list
  end

  defp halt(message, status \\ 2) do
    IO.write(:stderr, message <> "\n")
    System.halt(status)
  end
end

Cartographer.ElixirExtractCLI.main(System.argv())
