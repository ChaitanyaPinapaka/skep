interface User {
  id: string;
  name: string;
  email: string;
}

type UserRole = "admin" | "user" | "guest";

class UserService {
  getUser(id: string): User {
    return { id, name: "test", email: "test@test.com" };
  }

  deleteUser(id: string): void {
    console.log(`deleted ${id}`);
  }
}

export function createApp(): void {
  const svc = new UserService();
  svc.getUser("1");
}

export const fetchUsers = (limit: number): User[] => {
  return [];
};
