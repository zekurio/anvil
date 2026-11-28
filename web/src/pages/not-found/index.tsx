import { useTitle } from "../../lib/title";
import { EmptyState, LinkButton, Topbar } from "../../ui";

/** Any address no route matches. */
export default function NotFoundPage() {
  useTitle(() => "Not found");
  return (
    <>
      <Topbar title="Not found" />
      <EmptyState title="This page does not exist" description="Check the address, or pick a view from the navigation.">
        <LinkButton href="/">Open the first library</LinkButton>
      </EmptyState>
    </>
  );
}
