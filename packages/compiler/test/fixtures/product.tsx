import { useEffect, useState } from 'react'
import { ClientOnly, SafeHTML, string } from '@gobeyond/react'

type Product = {
  slug: string
  name: string
  price: number
  available: boolean
  features: { id: string; label: string }[]
  descriptionHTML: string
}

function Price({ amount }: { amount: number }) {
  return (
    <data className="price" value={amount}>
      {string(amount)}
    </data>
  )
}

export default function ProductPage(props: {
  product: Product
  showBuy: boolean
}) {
  const [quantity, setQuantity] = useState(1)
  useEffect(() => {
    document.title = props.product.name
  }, [props.product.name])

  return (
    <main data-slug={props.product.slug}>
      <h1>{props.product.name}</h1>
      <Price amount={props.product.price} />
      {props.product.available && <p className="stock">In stock</p>}
      <ul>
        {props.product.features.map((feature, index) => (
          <li key={feature.id} data-index={index}>
            {feature.label}
          </li>
        ))}
      </ul>
      <SafeHTML as="div" value={props.product.descriptionHTML} />
      {props.showBuy ? (
        <button
          disabled={!props.product.available}
          onClick={() => setQuantity(quantity + 1)}
        >
          Add {quantity}
        </button>
      ) : (
        <p>Contact us</p>
      )}
      <ClientOnly
        fallback={<div className="map-placeholder">Map unavailable</div>}
      >
        <ExternalMap center={window.location.hash} />
      </ClientOnly>
    </main>
  )
}
