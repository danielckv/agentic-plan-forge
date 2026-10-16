import js from "@eslint/js";
import react from "eslint-plugin-react";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: ["dist/", "coverage/", "playwright-report/", "test-results/", "node_modules/"],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    plugins: {
      react,
      "react-hooks": reactHooks,
    },
    settings: {
      react: { version: "detect" },
    },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
      "react/no-danger": "error",
      "react/jsx-uses-react": "off",
      "react/react-in-jsx-scope": "off",
    },
    languageOptions: {
      globals: {
        // Vite client types cover import.meta; browser globals come from DOM lib.
        window: "readonly",
        document: "readonly",
        console: "readonly",
      },
    },
  },
  {
    // MermaidSvg.tsx owns the only dangerouslySetInnerHTML in the tree:
    // the sanitized-SVG insertion point for rendered diagrams.
    files: ["src/components/MermaidSvg.tsx"],
    rules: {
      "react/no-danger": "off",
    },
  },
);
