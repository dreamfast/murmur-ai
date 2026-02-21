import js from "@eslint/js";
import pluginVue from "eslint-plugin-vue";
import globals from "globals";

export default [
  js.configs.recommended,
  ...pluginVue.configs["flat/recommended"],
  {
    languageOptions: {
      globals: {
        ...globals.browser,
      },
    },
    rules: {
      // Allow v-html — we escape HTML before rendering markdown.
      "vue/no-v-html": "off",
      // Single-word component names are fine for views (Login, Chat, etc.).
      "vue/multi-word-component-names": "off",
      // We intentionally use control characters for IRC color codes and
      // markdown placeholder tokens (\x00, \x02, \x03, etc.).
      "no-control-regex": "off",
      // Formatting preferences — we use Prettier-style inline attributes.
      "vue/max-attributes-per-line": "off",
      "vue/singleline-html-element-content-newline": "off",
      // Allow self-closing on HTML elements (Vue convention).
      "vue/html-self-closing": "off",
    },
  },
];
