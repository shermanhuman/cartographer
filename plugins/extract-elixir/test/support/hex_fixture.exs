defmodule Cartographer.HexFixture do
  @moduledoc """
  Builds Hex packages for tests: a tarball laid out as Hex writes it
  (VERSION, CHECKSUM, metadata.config, contents.tar.gz) in a scratch Hex
  cache, and a `mix.lock` entry whose outer checksum is the tarball's
  SHA-256.
  """

  @doc """
  Writes each package `{name, version, %{"lib/x.ex" => source}}` to
  `hex_dir` and returns the lock text naming them all.
  """
  def write(hex_dir, packages, opts \\ []) do
    File.mkdir_p!(hex_dir)

    entries =
      for {name, version, files} <- packages do
        tar = tarball(files)
        File.write!(Path.join(hex_dir, "#{name}-#{version}.tar"), tar)
        outer = if opts[:wrong_checksum] == name, do: String.duplicate("0", 64), else: sha(tar)
        ~s(  "#{name}": {:hex, :#{name}, "#{version}", "#{String.duplicate("a", 64)}", [:mix], [], "hexpm", "#{outer}"},)
      end

    "%{\n" <> Enum.join(entries, "\n") <> "\n}\n"
  end

  defp tarball(files) do
    contents = targz(for {p, src} <- files, do: {String.to_charlist(p), src})

    outer = [
      {~c"VERSION", "3"},
      {~c"CHECKSUM", String.upcase(sha(contents))},
      {~c"metadata.config", ~s({<<"name">>,<<"fixture">>}.\n)},
      {~c"contents.tar.gz", contents}
    ]

    tar(outer, [])
  end

  defp targz(files), do: tar(files, [:compressed])

  defp tar(files, opts) do
    path = Path.join(System.tmp_dir!(), "hexfix-#{System.unique_integer([:positive])}.tar")
    :ok = :erl_tar.create(String.to_charlist(path), files, opts)
    bin = File.read!(path)
    File.rm!(path)
    bin
  end

  defp sha(bin), do: :crypto.hash(:sha256, bin) |> Base.encode16(case: :lower)
end
