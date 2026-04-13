# Skep docs (site)

The public Skep documentation site. Built with
[Astro Starlight](https://starlight.astro.build/), deployed to GitHub
Pages at [https://skep.sh](https://skep.sh).

## Local preview

```bash
cd site
npm install
npm run dev
```

Then open http://localhost:4321/ in your browser. No virtualenv or
Python toolchain needed — Node handles isolation via `node_modules/`.

## Build a static bundle

```bash
npm run build
```

Output lands in `site/dist/`. Preview the production bundle with:

```bash
npm run preview
```

## Deployment

Deployment to GitHub Pages is automatic on every push to `main` that
touches `site/**` or `.github/workflows/docs.yml`. The workflow
builds the site, uploads `dist/` as a Pages artifact, and deploys via
GitHub's first-party `actions/deploy-pages` action.

## DNS

The site is served from `https://skep.sh`. GitHub Pages custom
domains on an apex (non-`www`) need **A records**, not a CNAME —
the DNS spec forbids CNAMEs at a zone apex because they'd conflict
with SOA/NS records. Add four A records at your registrar pointing
`skep.sh` at GitHub's Pages IPs:

```
skep.sh.    A    185.199.108.153
skep.sh.    A    185.199.109.153
skep.sh.    A    185.199.110.153
skep.sh.    A    185.199.111.153
```

Optionally add a `www` CNAME so `www.skep.sh` also works:

```
www.skep.sh.    CNAME    chaitanyapinapaka.github.io.
```

The `public/CNAME` file in this directory is published as the
top-level `CNAME` of the built site so GitHub Pages picks up the
custom domain on deploy. Enable HTTPS in repo Settings → Pages once
DNS has propagated (`dig skep.sh +short` should return the four
IPs above before you enforce HTTPS).

Current apex A-record targets: check
[GitHub Pages DNS docs](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site#configuring-an-apex-domain)
before flipping DNS — GitHub rotates these rarely but they have
changed.

## Layout

```
site/
├── astro.config.mjs       # Starlight config + sidebar
├── package.json           # Astro + Starlight deps
├── public/                # Favicon, logo, OG image, CNAME, robots.txt
└── src/
    ├── content/
    │   ├── config.ts      # Starlight content collection
    │   └── docs/          # All MDX/Markdown pages
    └── styles/
        └── skep-theme.css # Amber/honey palette overrides
```
