# Unpacks each verified Hex tarball a lock pins into <project>/deps/<lock key>,
# with the hex_metadata.config Hex writes. Usage: elixir stage_deps.exs <project> <hex dir>
[proj, hexdir] = System.argv()
{:ok, {:%{}, _, entries}} = Code.string_to_quoted(File.read!(Path.join(proj, "mix.lock")))
for {key, {:{}, _, [:hex, pkg, vsn, _inner, _m, _d, "hexpm", outer | _]}} <- entries do
  tar = Path.join(hexdir, "#{pkg}-#{vsn}.tar")
  bin = File.read!(tar)
  ^outer = :crypto.hash(:sha256, bin) |> Base.encode16(case: :lower)
  {:ok, files} = :erl_tar.extract({:binary, bin}, [:memory])
  {_, contents} = List.keyfind(files, ~c"contents.tar.gz", 0)
  {_, meta} = List.keyfind(files, ~c"metadata.config", 0)
  dest = Path.join([proj, "deps", to_string(key)])
  File.mkdir_p!(dest)
  :ok = :erl_tar.extract({:binary, contents}, [:compressed, {:cwd, String.to_charlist(dest)}])
  File.write!(Path.join(dest, "hex_metadata.config"), meta)
end
IO.puts("staged #{length(entries)} lock entries")
