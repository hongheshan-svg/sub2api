import landing from './landing'
import common from './common'
import dashboard from './dashboard'
import channelMonitorV2 from './channelMonitorV2'
import batchImage from './batchImage'
import admin from './admin'
import misc from './misc'
import fork from './fork'

export default {
  ...landing,
  ...common,
  ...dashboard,
  ...channelMonitorV2,
  ...batchImage,
  admin,
  ...misc,
  // Fork-only namespaces (invoice / notifications / imageGeneration). Kept last
  // so upstream domain modules stay pristine across syncs. See ./fork.ts.
  ...fork,
}
