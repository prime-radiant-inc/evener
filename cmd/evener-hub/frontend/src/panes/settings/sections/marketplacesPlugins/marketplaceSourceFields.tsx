// The source group both marketplace forms build: the kind radios and the one
// address field that kind takes. Local to the marketplace section because the
// directory picker binds to the host-scoped extensions store.

import { useId } from "react";
import { FormRow, Input, RadioGroup } from "../../../../widgets";
import { PathFieldWithPaths } from "../launchShared/fields";
import { useExtensionsHostStore, useHostDirectoryActions } from "./hostStore";
import { MARKETPLACE_SOURCE_OPTIONS, type MarketplaceSourceKind } from "./marketplaceEdit";

/** The fields the two forms keep in their own draft; `ref` and `name` are not
 * part of the source group (the edit form cannot change a ref). */
export interface MarketplaceSourceValue {
  kind: MarketplaceSourceKind;
  url: string;
  repo: string;
  path: string;
}

export function MarketplaceSourceFields({
  value,
  onChange,
  disabled = false,
}: {
  value: MarketplaceSourceValue;
  onChange: (patch: Partial<MarketplaceSourceValue>) => void;
  disabled?: boolean;
}) {
  const store = useExtensionsHostStore();
  const directory = useHostDirectoryActions();
  const ids = useId();

  return (
    <>
      <RadioGroup
        label="Source"
        value={value.kind}
        onChange={(kind) => onChange({ kind: kind as MarketplaceSourceKind })}
        options={MARKETPLACE_SOURCE_OPTIONS}
        disabled={disabled}
      />
      {value.kind === "url" && (
        <FormRow label="Git URL" htmlFor={`${ids}-url`}>
          <Input
            id={`${ids}-url`}
            value={value.url}
            onChange={(event) => onChange({ url: event.target.value })}
            placeholder="https://github.com/owner/repo.git"
            disabled={disabled}
          />
        </FormRow>
      )}
      {value.kind === "github" && (
        <FormRow label="owner/repo" htmlFor={`${ids}-repo`}>
          <Input
            id={`${ids}-repo`}
            value={value.repo}
            onChange={(event) => onChange({ repo: event.target.value })}
            placeholder="owner/repo"
            disabled={disabled}
          />
        </FormRow>
      )}
      {value.kind === "directory" && (
        <FormRow label="Local path" htmlFor={`${ids}-path`}>
          <PathFieldWithPaths
            ariaLabel="Local path"
            id={`${ids}-path`}
            value={value.path}
            onChange={(path) => onChange({ path })}
            kind="dir"
            paths={{
              directory,
              complete: (prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles),
            }}
            placeholder="/absolute/path"
            disabled={disabled}
          />
        </FormRow>
      )}
    </>
  );
}
