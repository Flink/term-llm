export default {
  extends: ['stylelint-config-standard'],
  ignoreFiles: ['dist/**', 'coverage/**'],
  rules: {
    // `app.css` is a manifest of bundler-resolved module imports. The bare
    // string form is what Vite resolves; `url()` would risk being treated as an
    // external request.
    'import-notation': 'string',
    // The migrated stylesheet intentionally keeps prefixed fallbacks and a
    // final integration layer that overrides earlier legacy selectors.
    'declaration-property-value-keyword-no-deprecated': null,
    'no-descending-specificity': null,
    'no-duplicate-selectors': null,
    'property-no-deprecated': null,
    'property-no-vendor-prefix': null,
    'selector-class-pattern': null,
    'value-no-vendor-prefix': null,
    // Corners come from the shared scale in src/styles/base/radius.css; only
    // 0, inherit and 50% (circles) may be literal.
    'declaration-property-value-disallowed-list': [
      { '/^border(-[a-z]+)*-radius$/': ['/\\d(px|rem|em)\\b/'] },
      { message: (property) => `Use a --radius-* token for ${property}, not a literal size.` },
    ],
  },
};
