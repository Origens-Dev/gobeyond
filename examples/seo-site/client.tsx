import { bootstrap } from "@gobeyond/react/browser";

import AccountPage from "./app/account/page.js";
import ArticlePage from "./app/articles/[slug]/page.js";
import CategoryPage from "./app/category/[page]/page.js";
import EnglishArticlePage from "./app/en/articles/[slug]/page.js";
import FrenchArticlePage from "./app/fr/articles/[slug]/page.js";
import LocationPage from "./app/locations/[slug]/page.js";
import HomePage from "./app/page.js";
import ProductPage from "./app/products/[slug]/page.js";

bootstrap({
  routes: {
    r_route_8a5edab2: HomePage,
    r_account_441bb226: AccountPage,
    r_articles__slug_c2f99372: ArticlePage,
    r_category__page_05ecbf63: CategoryPage,
    r_en_articles__slug_2da8a82a: EnglishArticlePage,
    r_fr_articles__slug_fee2939e: FrenchArticlePage,
    r_locations__slug_730658f7: LocationPage,
    r_products__slug_3e2e8eb9: ProductPage,
  },
});
