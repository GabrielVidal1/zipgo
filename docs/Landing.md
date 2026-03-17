# Landing page

When no `root` site is deployed, zipgo automatically generates a landing page that links to all hosted sites.

![zipgo landing page](./landing.png)

## When it appears

The landing page is shown at the root URL (`https://yourdomain.com` or `http://localhost:9000`) whenever the `apps/` directory does not contain a folder named `root`. As soon as you deploy a site named `root`, it takes over and the landing page is no longer generated.

## What it shows

Each hosted site gets a card displaying:

- A coloured avatar with the site's initial (colour is derived deterministically from the name)
- The site name and its public URL
- The page title and description, scraped from the site's `index.html` `<head>` — it reads `<title>`, `<meta name="description">`, `og:title`, and `og:description`
- A `STATIC` or `SPA` badge

Clicking a card navigates directly to that site.

## Customising how your site appears

The landing page reads standard HTML meta tags — no zipgo-specific config needed. To control how your site is listed, set these tags in your `index.html`:

```html
<title>My App</title>
<meta name="description" content="A short description shown on the card." />

<!-- Open Graph fallbacks (used if the above are absent) -->
<meta property="og:title" content="My App" />
<meta
  property="og:description"
  content="A short description shown on the card."
/>
```

## Replacing it

Deploy any site named `root` to replace the landing page with your own content:

```
apps/
└── root/
    └── index.html   ← served at the apex domain / port 9000
```

The generated landing page is written to `/tmp/zipgo-landing/` and is recreated on every reload, so it always reflects the current set of deployed sites.
