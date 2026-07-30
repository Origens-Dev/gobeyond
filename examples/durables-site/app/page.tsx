import "./site.css";

export default function HomePage() {
  return (
    <main>
      <h1>Durables example</h1>
      <p>
        Try a couple of Temporal workflows locally. Open{" "}
        <a href="/durables">/durables</a>, then peek at runs in the Temporal UI
        at <a href="http://localhost:8233">localhost:8233</a>.
      </p>
    </main>
  );
}
