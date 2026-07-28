// Watch-side app entry point. Fires before any page is shown / after the
// last page closes. Not used for anything beyond logging right now - all
// the actual behavior lives in page/index.js and page/list.js.
App({
  globalData: {},
  onCreate(options) {
    console.log('app on create invoke')
  },

  onDestroy(options) {
    console.log('app on destroy invoke')
  }
})