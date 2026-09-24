# Release metadata from the source flake's last-change timestamp, in UTC.
{ flake }:
let
  stamp = flake.lastModifiedDate;
  part = start: length: builtins.substring start length stamp;
in
{
  version = "${part 2 6}.${part 8 4}";
  revision = flake.rev or "dirty";
}
