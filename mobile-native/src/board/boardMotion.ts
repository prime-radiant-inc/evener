import { LinearTransition } from "react-native-reanimated";

/** Rows move to their new places with a 250ms spring (spec 16.6), critically
 * damped so nothing bounces. BoardScreen drops it under Reduce Motion. */
export const ROW_MOVE = LinearTransition.springify().duration(250).dampingRatio(1);
