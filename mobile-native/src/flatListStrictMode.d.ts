// FlatList's strictMode memoizes its item renderer, so a cell re-renders only
// for a new renderItem, row or extraData. React Native documents it on
// FlatList (Libraries/Lists/FlatList.js, "Enable an optimization to memoize
// the item renderer") but leaves it out of the TypeScript types.
import "react-native";

declare module "react-native" {
	interface FlatListProps<ItemT> {
		strictMode?: boolean;
	}
}
