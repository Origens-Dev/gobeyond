import { ClientOnly } from "@gobeyond/react";

export interface LocationMapProps {
  name: string;
  mapHref: string;
}

function BrowserMap({ name }: { name: string }) {
  return <div role="img" aria-label={`Interactive map for ${name}`} />;
}

export function LocationMap({ name, mapHref }: LocationMapProps) {
  return (
    <ClientOnly fallback={<a href={mapHref}>Open {name} in maps</a>}>
      <BrowserMap name={name} />
    </ClientOnly>
  );
}
