export interface PaginationProps {
  currentPage: number;
  previousHref?: string;
  nextHref?: string;
}

export function Pagination({
  currentPage,
  previousHref,
  nextHref,
}: PaginationProps) {
  return (
    <nav aria-label="Category pages">
      {previousHref ? <a href={previousHref}>Previous page</a> : null}
      <span aria-current="page">Page {currentPage}</span>
      {nextHref ? <a href={nextHref}>Next page</a> : null}
    </nav>
  );
}
