# aura for Silo

A [Silo](https://siloserver.org) plugin that gives Silo the MediUX artwork from your [aura](https://github.com/mediux-team/AURA) server. Your saved sets come first, then your favorite creators, then the sets with the most downloads on MediUX.

When Silo uses one of aura's images, aura saves it in that image's set and applies it through its Download Queue, to every media server it manages.

## What Silo gets

| Silo asks for | aura gives |
| --- | --- |
| A refresh of an item with a saved set | The saved set's images, for the image types you selected |
| A refresh of an item without one | Your favorite creators' images only, ranked. Without one, nothing, and Silo uses its next provider |
| The image picker in **Edit Metadata > Images** | Every image, ranked, for the movie or show and for each season (needs a Silo version with [silo-server#3145](https://github.com/Silo-Server/silo-server/pull/3145)) |

Silo downloads previews and images straight from MediUX. When Silo downloads an image to use it, the plugin tells aura, and aura saves that image's type from its set:

| Silo used | aura saves |
| --- | --- |
| A poster | The set's poster |
| A backdrop | The set's backdrop |
| A season poster | The set's season posters (or the Specials poster) |
| An episode's title card | The set's title cards |

- Turn on **Apply Whole Set** to also save the image types in your Download Defaults from the set.
- A type saved from the set takes over that type from the item's other saved sets, and the types the set already has saved are kept.
- From a refresh, only a favorite creator's set is saved. Anything you pick in the image picker is saved.
- Silo's image picker asks plugins for episodes with the show's images, so title cards come from a refresh.

When aura applies a set to Silo itself, it clears Silo's image lock and refreshes the item, so Silo gets the images from this plugin and later refreshes keep them.

## Setup

1. In Silo, add this plugin repository, then install **aura** from it and enable it. Silo then offers new versions as they're released.

   ```text
   https://raw.githubusercontent.com/xmoosex/aura-silo-plugin/main/index.json
   ```

   Or download the zip from the [latest release](https://github.com/xmoosex/aura-silo-plugin/releases/latest) and upload it on Silo's **Plugins** page.
2. Enter your **aura URL**, the address the plugin reaches aura at, like `http://192.168.1.30:3000`, and your **aura API key** from aura's **Settings > Authentication**. Leave the key empty when aura has no login. Only the plugin talks to aura, so a LAN address is fine. Turn on **Auto Download** to save those sets with Auto Download on, so aura keeps them up to date, and **Apply Whole Set** to save the image types in your Download Defaults with each image you use.
3. In each library's providers, put **aura** first, right after the local images or NFO provider.
4. Refresh an item to apply its artwork. Items whose artwork you set by hand in Silo have their images locked, so a refresh skips them until aura unlocks them.

## Building

```sh
make check   # vet, tests and gofmt
make dist    # dist/<version>/aura-silo-plugin-<version>-linux-amd64.zip
```

## Releasing

1. Raise `version` in `manifest.json` on `main`.
2. Push the matching tag on that commit, like `v1.0.0`.

The release workflow then runs Silo's shared plugin release workflow, which builds, signs and publishes the binaries and tells Silo's approved community catalog about the release. It also adds the zip for installing by hand to the release, and updates `index.json` on `main` with `scripts/index.py`.

## Support

Open an issue in this repository. Problems with aura itself go to [aura's issues](https://github.com/mediux-team/AURA/issues).

## License

[MIT](LICENSE)
