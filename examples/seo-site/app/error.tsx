export interface ErrorPageProps {
  requestId: string;
}

export default function ErrorPage({ requestId }: ErrorPageProps) {
  return (
    <section role="alert">
      <h1>Something went wrong</h1>
      <p>Try again later. Request ID: {requestId}</p>
    </section>
  );
}
